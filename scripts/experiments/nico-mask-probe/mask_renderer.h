#ifndef NICO_MASK_PROBE_MASK_RENDERER_H
#define NICO_MASK_PROBE_MASK_RENDERER_H

// NMF1 low-bit mask renderer used only by the AVFrame comparison probe.
// This file deliberately has no dependency on the production renderer.

#include <algorithm>
#include <array>
#include <chrono>
#include <cstddef>
#include <cmath>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <limits>
#include <sstream>
#include <stdexcept>
#include <string>
#include <unordered_map>
#include <vector>

namespace nm {

struct Command {
  uint32_t id = 0;
  float rect[4] = {};
  float proj[16] = {};
  float color[4] = {};
};
static_assert(sizeof(Command) == 100, "NMF1 command ABI");

struct Frame {
  std::vector<Command> commands;
};

struct Texture {
  uint32_t id = 0;
  uint32_t width = 0;
  uint32_t height = 0;
  uint32_t kind = 0;
  // R is the low byte: r | (g << 8) | (b << 16) | (a << 24).
  uint32_t fillRGBA = 0;
  uint32_t outlineRGBA = 0;
  uint32_t outlineRadiusPx = 0;
  std::vector<uint8_t> payload;
};

struct Scene {
  uint32_t width = 0;
  uint32_t height = 0;
  uint32_t fps_num = 0;
  uint32_t fps_den = 0;
  std::vector<Texture> textures;
  std::vector<Frame> frames;
};

namespace detail {

constexpr uint32_t kMagic = 0x31464d4e; // "NMF1" in little endian
constexpr uint32_t kMaxWidth = 3840;
constexpr uint32_t kMaxHeight = 2160;
constexpr uint32_t kMaxTextureDimension = 16384;
constexpr uint64_t kMaxTextureBytes = 128ull * 1024ull * 1024ull;
constexpr uint64_t kMaxTextureTotal = 512ull * 1024ull * 1024ull;
constexpr uint32_t kMaxTextures = 10000;
constexpr uint32_t kMaxFrames = 100000;
constexpr uint64_t kMaxCommands = 10000000;
constexpr uint64_t kMaxFileBytes = 1ull * 1024ull * 1024ull * 1024ull;
constexpr uint32_t kMaxOutlineRadius = 64;

inline uint64_t checked_mul(uint64_t a, uint64_t b, const char* what) {
  if (a != 0 && b > std::numeric_limits<uint64_t>::max() / a)
    throw std::runtime_error(std::string("NMF1 overflow: ") + what);
  return a * b;
}

class Reader {
 public:
  explicit Reader(const std::vector<uint8_t>& data) : data_(data) {}

  size_t remaining() const { return data_.size() - pos_; }
  size_t position() const { return pos_; }

  uint32_t u32(const char* what) {
    require(4, what);
    const uint32_t v = uint32_t(data_[pos_]) |
                       (uint32_t(data_[pos_ + 1]) << 8) |
                       (uint32_t(data_[pos_ + 2]) << 16) |
                       (uint32_t(data_[pos_ + 3]) << 24);
    pos_ += 4;
    return v;
  }

  float f32(const char* what) {
    const uint32_t bits = u32(what);
    float v;
    std::memcpy(&v, &bits, sizeof(v));
    return v;
  }

  std::vector<uint8_t> bytes(size_t n, const char* what) {
    require(n, what);
    std::vector<uint8_t> out(data_.begin() + static_cast<ptrdiff_t>(pos_),
                             data_.begin() + static_cast<ptrdiff_t>(pos_ + n));
    pos_ += n;
    return out;
  }

 private:
  void require(size_t n, const char* what) {
    if (n > remaining())
      throw std::runtime_error(std::string("NMF1 truncated ") + what);
  }
  const std::vector<uint8_t>& data_;
  size_t pos_ = 0;
};

inline std::vector<uint8_t> read_file(const std::string& path) {
  std::ifstream in(path, std::ios::binary | std::ios::ate);
  if (!in) throw std::runtime_error("NMF1: cannot open scene: " + path);
  const std::streampos end = in.tellg();
  if (end < 0 || static_cast<uint64_t>(end) > kMaxFileBytes)
    throw std::runtime_error("NMF1: file size bound");
  std::vector<uint8_t> data(static_cast<size_t>(end));
  in.seekg(0, std::ios::beg);
  if (!data.empty() && !in.read(reinterpret_cast<char*>(data.data()), static_cast<std::streamsize>(data.size())))
    throw std::runtime_error("NMF1: scene read failed");
  return data;
}

inline void require_finite(const float* p, size_t n, const char* what) {
  for (size_t i = 0; i < n; ++i)
    if (!std::isfinite(p[i]))
      throw std::runtime_error(std::string("NMF1 non-finite ") + what);
}

inline uint64_t pixels_for(const Texture& t) {
  if (!t.width || !t.height || t.width > kMaxTextureDimension || t.height > kMaxTextureDimension)
    throw std::runtime_error("NMF1 texture dimension bound");
  return checked_mul(t.width, t.height, "texture pixels");
}

inline size_t packed_bytes(uint32_t w, uint32_t h, int bits) {
  const uint64_t rowBits = checked_mul(w, static_cast<uint64_t>(bits), "packed row");
  const uint64_t rowBytes = (rowBits + 7u) / 8u;
  const uint64_t total = checked_mul(rowBytes, h, "packed payload");
  if (total > std::numeric_limits<size_t>::max()) throw std::runtime_error("NMF1 packed payload overflow");
  return static_cast<size_t>(total);
}

inline uint8_t quantize(uint8_t a, int bits) {
  const unsigned levels = (1u << bits) - 1u;
  return static_cast<uint8_t>((unsigned(a) * levels + 127u) / 255u);
}

inline uint8_t expand(uint8_t q, int bits) {
  const unsigned levels = (1u << bits) - 1u;
  return static_cast<uint8_t>((unsigned(q) * 255u + levels / 2u) / levels);
}

inline std::vector<uint8_t> pack_a8(const std::vector<uint8_t>& a8, uint32_t w, uint32_t h, int bits) {
  const size_t expected = static_cast<size_t>(checked_mul(w, h, "A8 payload"));
  if (a8.size() != expected) throw std::runtime_error("NMF1 A8 payload length");
  std::vector<uint8_t> out(packed_bytes(w, h, bits), 0);
  const unsigned mask = (1u << bits) - 1u;
  const size_t rowBytes = (static_cast<size_t>(w) * static_cast<size_t>(bits) + 7u) / 8u;
  for (uint32_t y = 0; y < h; ++y) {
    unsigned bit = 0;
    for (uint32_t x = 0; x < w; ++x, bit += static_cast<unsigned>(bits)) {
      const uint8_t q = quantize(a8[static_cast<size_t>(y) * w + x], bits);
      const size_t byte = static_cast<size_t>(y) * rowBytes + bit / 8u;
      const unsigned shift = 8u - bits - (bit % 8u);
      out[byte] = static_cast<uint8_t>(out[byte] | ((q & mask) << shift));
    }
  }
  return out;
}

inline std::vector<uint8_t> unpack_a8(const std::vector<uint8_t>& packed, uint32_t w, uint32_t h, int bits) {
  if (packed.size() != packed_bytes(w, h, bits)) throw std::runtime_error("NMF1 packed payload length");
  std::vector<uint8_t> out(static_cast<size_t>(checked_mul(w, h, "expanded A8")));
  const size_t rowBytes = (static_cast<size_t>(w) * static_cast<size_t>(bits) + 7u) / 8u;
  const unsigned mask = (1u << bits) - 1u;
  for (uint32_t y = 0; y < h; ++y) {
    unsigned bit = 0;
    for (uint32_t x = 0; x < w; ++x, bit += static_cast<unsigned>(bits)) {
      const size_t byte = static_cast<size_t>(y) * rowBytes + bit / 8u;
      const unsigned shift = 8u - bits - (bit % 8u);
      const uint8_t q = static_cast<uint8_t>((packed[byte] >> shift) & mask);
      out[static_cast<size_t>(y) * w + x] = expand(q, bits);
    }
  }
  return out;
}

inline uint8_t chan(uint32_t rgba, unsigned i) {
  return static_cast<uint8_t>((rgba >> (8u * i)) & 0xffu);
}

inline float clamp01(float x) { return std::max(0.0f, std::min(1.0f, x)); }

inline std::array<float, 4> premul(uint32_t rgba) {
  const float a = float(chan(rgba, 3)) / 255.0f;
  return {float(chan(rgba, 0)) / 255.0f * a,
          float(chan(rgba, 1)) / 255.0f * a,
          float(chan(rgba, 2)) / 255.0f * a, a};
}

inline std::array<float, 4> over(const std::array<float, 4>& top,
                                 const std::array<float, 4>& bottom) {
  const float inv = 1.0f - clamp01(top[3]);
  return {top[0] + bottom[0] * inv, top[1] + bottom[1] * inv,
          top[2] + bottom[2] * inv, top[3] + bottom[3] * inv};
}

struct CachedTexture {
  uint32_t width = 0, height = 0;
  uint32_t kind = 0;
  std::vector<uint8_t> packed;
  std::vector<uint8_t> expanded;
  std::vector<uint8_t> outline;
  std::vector<uint8_t> rgba;
  std::array<float, 4> fill = {};
  std::array<float, 4> outlineColor = {};
};

inline std::vector<uint8_t> dilate_disk(const std::vector<uint8_t>& source,
                                        uint32_t w, uint32_t h, uint32_t radius) {
  std::vector<uint8_t> out(static_cast<size_t>(w) * h, 0);
  if (source.empty() || radius == 0) return source;
  // Only a radius reaching the image diagonal reduces to the global maximum.
  // (radius >= max(width,height) is insufficient for a square texture.)
  const uint64_t maxDx = w - 1u, maxDy = h - 1u;
  if (uint64_t(radius) * radius >= maxDx * maxDx + maxDy * maxDy) {
    const uint8_t m = *std::max_element(source.begin(), source.end());
    std::fill(out.begin(), out.end(), m);
    return out;
  }
  const int r = static_cast<int>(radius);
  std::vector<std::pair<int, int>> offsets;
  const int rr = r * r;
  for (int dy = -r; dy <= r; ++dy)
    for (int dx = -r; dx <= r; ++dx)
      if (dx * dx + dy * dy <= rr) offsets.emplace_back(dx, dy);
  for (uint32_t y = 0; y < h; ++y) {
    for (uint32_t x = 0; x < w; ++x) {
      uint8_t m = 0;
      for (const auto [dx, dy] : offsets) {
        const int sx = std::max(0, std::min(static_cast<int>(w) - 1, static_cast<int>(x) + dx));
        const int sy = std::max(0, std::min(static_cast<int>(h) - 1, static_cast<int>(y) + dy));
        m = std::max(m, source[static_cast<size_t>(sy) * w + static_cast<size_t>(sx)]);
      }
      out[static_cast<size_t>(y) * w + x] = m;
    }
  }
  return out;
}

inline float sample_a8(const std::vector<uint8_t>& image, uint32_t w, uint32_t h, float u, float v) {
  if (image.empty() || !w || !h) return 0.0f;
  float x = u * float(w) - 0.5f;
  float y = v * float(h) - 0.5f;
  x = std::max(0.0f, std::min(float(w - 1), x));
  y = std::max(0.0f, std::min(float(h - 1), y));
  const uint32_t x0 = static_cast<uint32_t>(std::floor(x));
  const uint32_t y0 = static_cast<uint32_t>(std::floor(y));
  const uint32_t x1 = std::min(w - 1, x0 + 1);
  const uint32_t y1 = std::min(h - 1, y0 + 1);
  const float fx = x - float(x0), fy = y - float(y0);
  const auto at = [&](uint32_t xx, uint32_t yy) { return float(image[static_cast<size_t>(yy) * w + xx]) / 255.0f; };
  return (at(x0, y0) * (1.0f - fx) + at(x1, y0) * fx) * (1.0f - fy) +
         (at(x0, y1) * (1.0f - fx) + at(x1, y1) * fx) * fy;
}

inline std::array<float, 4> sample_rgba(const std::vector<uint8_t>& image, uint32_t w,
                                         uint32_t h, float u, float v) {
  std::array<float, 4> out = {};
  if (image.empty() || !w || !h) return out;
  float x = std::max(0.0f, std::min(float(w - 1), u * float(w) - 0.5f));
  float y = std::max(0.0f, std::min(float(h - 1), v * float(h) - 0.5f));
  const uint32_t x0 = static_cast<uint32_t>(std::floor(x));
  const uint32_t y0 = static_cast<uint32_t>(std::floor(y));
  const uint32_t x1 = std::min(w - 1, x0 + 1);
  const uint32_t y1 = std::min(h - 1, y0 + 1);
  const float fx = x - float(x0), fy = y - float(y0);
  for (unsigned c = 0; c < 4; ++c) {
    const auto at = [&](uint32_t xx, uint32_t yy) { return float(image[(static_cast<size_t>(yy) * w + xx) * 4 + c]) / 255.0f; };
    out[c] = (at(x0, y0) * (1.0f - fx) + at(x1, y0) * fx) * (1.0f - fy) +
             (at(x0, y1) * (1.0f - fx) + at(x1, y1) * fx) * fy;
  }
  return out;
}

struct RectMap {
  float x0, y0, dx, dy;
};

inline RectMap map_rect(const Command& c, uint32_t outputW, uint32_t outputH) {
  const auto project = [&](float x, float y) {
    const float cx = c.proj[0] * x + c.proj[4] * y + c.proj[12];
    const float cy = c.proj[1] * x + c.proj[5] * y + c.proj[13];
    const float cw = c.proj[3] * x + c.proj[7] * y + c.proj[15];
    if (!std::isfinite(cx) || !std::isfinite(cy) || !std::isfinite(cw) || std::fabs(cw) < 1e-7f)
      throw std::runtime_error("NMF1 unsupported projection w");
    const float sx = (cx / cw + 1.0f) * float(outputW) * 0.5f;
    const float sy = (1.0f - cy / cw) * float(outputH) * 0.5f;
    if (!std::isfinite(sx) || !std::isfinite(sy))
      throw std::runtime_error("NMF1 unsupported projection viewport");
    return std::array<float, 2>{sx, sy};
  };
  const auto p00 = project(c.rect[0], c.rect[1]);
  const auto p10 = project(c.rect[0] + c.rect[2], c.rect[1]);
  const auto p01 = project(c.rect[0], c.rect[1] + c.rect[3]);
  const auto p11 = project(c.rect[0] + c.rect[2], c.rect[1] + c.rect[3]);
  const float eps = 1e-3f;
  if (std::fabs(p00[1] - p10[1]) > eps || std::fabs(p01[1] - p11[1]) > eps ||
      std::fabs(p00[0] - p01[0]) > eps || std::fabs(p10[0] - p11[0]) > eps ||
      std::fabs((p10[0] - p00[0]) - (p11[0] - p01[0])) > eps ||
      std::fabs((p01[1] - p00[1]) - (p11[1] - p10[1])) > eps)
    throw std::runtime_error("NMF1 unsupported rotation/shear/perspective");
  const float dx = p10[0] - p00[0], dy = p01[1] - p00[1];
  if (!std::isfinite(dx) || !std::isfinite(dy))
    throw std::runtime_error("NMF1 projection viewport overflow");
  return RectMap{p00[0], p00[1], dx, dy};
}

}  // namespace detail

inline Scene load_scene(const std::string& path) {
  const auto data = detail::read_file(path);
  detail::Reader r(data);
  Scene scene;
  if (r.u32("header magic") != detail::kMagic) throw std::runtime_error("NMF1 bad magic");
  scene.width = r.u32("width");
  scene.height = r.u32("height");
  scene.fps_num = r.u32("fps numerator");
  scene.fps_den = r.u32("fps denominator");
  const uint32_t textureCount = r.u32("texture count");
  const uint32_t frameCount = r.u32("frame count");
  if (!scene.width || !scene.height || scene.width > detail::kMaxWidth || scene.height > detail::kMaxHeight)
    throw std::runtime_error("NMF1 output dimension bound");
  if (!scene.fps_num || !scene.fps_den || scene.fps_num > 1000000 || scene.fps_den > 1000000)
    throw std::runtime_error("NMF1 fps bound");
  if (textureCount > detail::kMaxTextures || frameCount > detail::kMaxFrames)
    throw std::runtime_error("NMF1 count bound");
  scene.textures.reserve(textureCount);
  uint64_t totalTextureBytes = 0;
  std::unordered_map<uint32_t, bool> ids;
  for (uint32_t i = 0; i < textureCount; ++i) {
    Texture t;
    t.id = r.u32("texture id");
    t.width = r.u32("texture width");
    t.height = r.u32("texture height");
    t.kind = r.u32("texture kind");
    t.fillRGBA = r.u32("fill color");
    t.outlineRGBA = r.u32("outline color");
    t.outlineRadiusPx = r.u32("outline radius");
    const uint32_t payloadBytes = r.u32("payload length");
    const uint64_t pixels = detail::pixels_for(t);
    uint64_t expected = 0;
    if (t.kind == 1) expected = pixels;
    else if (t.kind == 0) expected = detail::checked_mul(pixels, 4, "RGBA payload");
    else throw std::runtime_error("NMF1 unknown texture kind");
    if (expected > detail::kMaxTextureBytes || payloadBytes != expected)
      throw std::runtime_error("NMF1 texture payload length");
    if (t.outlineRadiusPx > detail::kMaxOutlineRadius)
      throw std::runtime_error("NMF1 outline radius bound");
    if (!t.id || ids.find(t.id) != ids.end()) throw std::runtime_error("NMF1 duplicate or zero texture ID");
    ids.emplace(t.id, true);
    totalTextureBytes += expected;
    if (totalTextureBytes > detail::kMaxTextureTotal) throw std::runtime_error("NMF1 texture total bound");
    t.payload = r.bytes(static_cast<size_t>(expected), "texture payload");
    scene.textures.push_back(std::move(t));
  }
  scene.frames.reserve(frameCount);
  uint64_t totalCommands = 0;
  for (uint32_t fi = 0; fi < frameCount; ++fi) {
    Frame f;
    const uint32_t count = r.u32("command count");
    totalCommands += count;
    if (totalCommands > detail::kMaxCommands) throw std::runtime_error("NMF1 command count bound");
    if (uint64_t(count) * sizeof(Command) > r.remaining())
      throw std::runtime_error("NMF1 command payload length");
    f.commands.resize(count);
    for (Command& c : f.commands) {
      c.id = r.u32("command texture ID");
      for (float& v : c.rect) v = r.f32("command rect");
      for (float& v : c.proj) v = r.f32("command projection");
      for (float& v : c.color) v = r.f32("command color");
      if (c.id && ids.find(c.id) == ids.end()) throw std::runtime_error("NMF1 unknown texture ID");
      detail::require_finite(c.rect, 4, "rect");
      detail::require_finite(c.proj, 16, "projection");
      detail::require_finite(c.color, 4, "color");
    }
    scene.frames.push_back(std::move(f));
  }
  if (r.remaining() != 0) throw std::runtime_error("NMF1 trailing bytes");
  return scene;
}

struct Stats {
  // Renderer-owned cache only; Scene::Texture::payload remains separately
  // owned by the input Scene and is intentionally excluded here.
  uint64_t raw_mask_bytes = 0;
  uint64_t packed_mask_bytes = 0;
  uint64_t cache_bytes = 0;
  double pack_ms = 0.0;
  double expand_ms = 0.0;
  double outline_ms = 0.0;
};

class Renderer {
 public:
  Renderer(const Scene& scene, int bits) : scene_(scene), bits_(bits) {
    if (bits_ != 8 && bits_ != 4 && bits_ != 2 && bits_ != 1)
      throw std::runtime_error("NMF1 bits must be 8, 4, 2, or 1");
    if (!scene_.width || !scene_.height || scene_.width > detail::kMaxWidth || scene_.height > detail::kMaxHeight)
      throw std::runtime_error("NMF1 output dimension bound");
    const auto packStart = std::chrono::steady_clock::now();
    std::unordered_map<uint32_t, bool> sourceIDs;
    uint64_t sourceBytes = 0;
    for (const Texture& source : scene_.textures) {
      const uint64_t pixels = detail::pixels_for(source);
      const uint64_t expected = source.kind == 1 ? pixels :
          source.kind == 0 ? detail::checked_mul(pixels, 4, "RGBA payload") : 0;
      if (!source.id || !sourceIDs.emplace(source.id, true).second)
        throw std::runtime_error("NMF1 duplicate or zero texture ID");
      if (source.kind != 0 && source.kind != 1)
        throw std::runtime_error("NMF1 unknown texture kind");
      if (expected > detail::kMaxTextureBytes || source.payload.size() != expected)
        throw std::runtime_error("NMF1 texture payload length");
      sourceBytes += expected;
      if (sourceBytes > detail::kMaxTextureTotal)
        throw std::runtime_error("NMF1 texture total bound");
      if (source.outlineRadiusPx > detail::kMaxOutlineRadius)
        throw std::runtime_error("NMF1 outline radius bound");
      detail::CachedTexture t;
      t.width = source.width;
      t.height = source.height;
      t.kind = source.kind;
      t.fill = detail::premul(source.fillRGBA);
      t.outlineColor = detail::premul(source.outlineRGBA);
      if (source.kind == 1) {
        stats_.raw_mask_bytes += static_cast<uint64_t>(source.payload.size());
        t.packed = detail::pack_a8(source.payload, source.width, source.height, bits_);
        stats_.packed_mask_bytes += static_cast<uint64_t>(t.packed.size());
      } else {
        t.rgba = source.payload;
      }
      cache_.emplace(source.id, std::move(t));
    }
    const auto packEnd = std::chrono::steady_clock::now();
    stats_.pack_ms = std::chrono::duration<double, std::milli>(packEnd - packStart).count();
    const auto expandStart = std::chrono::steady_clock::now();
    for (auto& entry : cache_) {
      detail::CachedTexture& t = entry.second;
      if (t.kind != 1) continue;
      t.expanded = detail::unpack_a8(t.packed, t.width, t.height, bits_);
      stats_.cache_bytes += static_cast<uint64_t>(t.packed.size());
      stats_.cache_bytes += static_cast<uint64_t>(t.expanded.size());
    }
    const auto expandEnd = std::chrono::steady_clock::now();
    stats_.expand_ms = std::chrono::duration<double, std::milli>(expandEnd - expandStart).count();
    const auto outlineStart = std::chrono::steady_clock::now();
    for (const Texture& source : scene_.textures) {
      auto it = cache_.find(source.id);
      if (it == cache_.end() || it->second.kind != 1) continue;
      it->second.outline = detail::dilate_disk(it->second.expanded, source.width, source.height, source.outlineRadiusPx);
      stats_.cache_bytes += static_cast<uint64_t>(it->second.outline.size());
    }
    for (const auto& entry : cache_)
      if (entry.second.kind == 0)
        stats_.cache_bytes += static_cast<uint64_t>(entry.second.rgba.size());
    const auto outlineEnd = std::chrono::steady_clock::now();
    stats_.outline_ms = std::chrono::duration<double, std::milli>(outlineEnd - outlineStart).count();
    for (const Frame& frame : scene_.frames)
      for (const Command& c : frame.commands) {
        if (c.id && cache_.find(c.id) == cache_.end())
          throw std::runtime_error("NMF1 unknown texture ID");
        detail::require_finite(c.rect, 4, "rect");
        detail::require_finite(c.proj, 16, "projection");
        detail::require_finite(c.color, 4, "color");
        // Validate projection support before a potentially long run.
        (void)detail::map_rect(c, scene_.width, scene_.height);
      }
  }

  void draw(size_t frame_index, uint8_t* rgba, int stride) const {
    if (frame_index >= scene_.frames.size()) throw std::runtime_error("NMF1 frame index out of range");
    if (!rgba || stride < 0 || static_cast<uint64_t>(stride) < uint64_t(scene_.width) * 4ull)
      throw std::runtime_error("NMF1 invalid output stride");
    for (const Command& c : scene_.frames[frame_index].commands) {
      const detail::RectMap map = detail::map_rect(c, scene_.width, scene_.height);
      if (std::fabs(map.dx) < 1e-7f || std::fabs(map.dy) < 1e-7f) continue;
      const float minX = std::max(0.0f, std::min(map.x0, map.x0 + map.dx));
      const float maxX = std::min(float(scene_.width), std::max(map.x0, map.x0 + map.dx));
      const float minY = std::max(0.0f, std::min(map.y0, map.y0 + map.dy));
      const float maxY = std::min(float(scene_.height), std::max(map.y0, map.y0 + map.dy));
      if (minX >= maxX || minY >= maxY) continue;
      const uint32_t xBegin = static_cast<uint32_t>(std::max(0.0f, std::floor(minX)));
      const uint32_t xEnd = static_cast<uint32_t>(std::min(float(scene_.width), std::ceil(maxX)));
      const uint32_t yBegin = static_cast<uint32_t>(std::max(0.0f, std::floor(minY)));
      const uint32_t yEnd = static_cast<uint32_t>(std::min(float(scene_.height), std::ceil(maxY)));
      const detail::CachedTexture* texture = nullptr;
      if (c.id) {
        auto it = cache_.find(c.id);
        if (it == cache_.end()) throw std::runtime_error("NMF1 unknown texture ID");
        texture = &it->second;
      }
      const float commandScale = c.color[0];
      for (uint32_t y = yBegin; y < yEnd; ++y) {
        for (uint32_t x = xBegin; x < xEnd; ++x) {
          const float u = (float(x) + 0.5f - map.x0) / map.dx;
          const float v = (float(y) + 0.5f - map.y0) / map.dy;
          // The integer loop is a conservative clipped bounding box. Apply
          // the same half-open rectangle coverage rule as the GPU rasterizer
          // before sampling, so an adjacent pixel is never edge sampled.
          if (u < 0.0f || u >= 1.0f || v < 0.0f || v >= 1.0f) continue;
          std::array<float, 4> src = {};
          if (!texture) {
            src = {detail::clamp01(c.color[0]), detail::clamp01(c.color[1]),
                   detail::clamp01(c.color[2]), detail::clamp01(c.color[3])};
          } else if (texture->kind == 0) {
            src = detail::sample_rgba(texture->rgba, texture->width, texture->height, u, v);
            for (float& channel : src) channel *= detail::clamp01(commandScale);
          } else {
            const float fillMask = detail::sample_a8(texture->expanded, texture->width, texture->height, u, v);
            const float outlineMask = detail::sample_a8(texture->outline, texture->width, texture->height, u, v);
            std::array<float, 4> fill = texture->fill;
            std::array<float, 4> outline = texture->outlineColor;
            for (unsigned channel = 0; channel < 4; ++channel) {
              fill[channel] *= fillMask;
              outline[channel] *= outlineMask;
            }
            src = detail::over(fill, outline);
            for (float& channel : src) channel *= detail::clamp01(commandScale);
          }
          uint8_t* dstBytes = rgba + static_cast<size_t>(y) * static_cast<size_t>(stride) + static_cast<size_t>(x) * 4;
          std::array<float, 4> dst = {float(dstBytes[0]) / 255.0f, float(dstBytes[1]) / 255.0f,
                                      float(dstBytes[2]) / 255.0f, float(dstBytes[3]) / 255.0f};
          const auto out = detail::over(src, dst);
          for (unsigned channel = 0; channel < 4; ++channel)
            dstBytes[channel] = static_cast<uint8_t>(detail::clamp01(out[channel]) * 255.0f + 0.5f);
        }
      }
    }
  }

  const Stats& stats() const { return stats_; }

 private:
  const Scene& scene_;
  int bits_;
  std::unordered_map<uint32_t, detail::CachedTexture> cache_;
  mutable Stats stats_;
};

}  // namespace nm

#endif
