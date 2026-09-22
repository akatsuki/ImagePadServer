#include "mask_renderer.h"

#include <cstdio>
#include <cstdlib>
#include <iostream>
#include <string>

namespace {

using nm::Command;
using nm::Frame;
using nm::Scene;
using nm::Texture;

void check(bool condition, const std::string& message) {
  if (!condition) throw std::runtime_error("selftest: " + message);
}

void put_u32(std::vector<uint8_t>& out, uint32_t v) {
  out.push_back(static_cast<uint8_t>(v));
  out.push_back(static_cast<uint8_t>(v >> 8));
  out.push_back(static_cast<uint8_t>(v >> 16));
  out.push_back(static_cast<uint8_t>(v >> 24));
}

void put_f32(std::vector<uint8_t>& out, float value) {
  uint32_t bits = 0;
  std::memcpy(&bits, &value, sizeof(bits));
  put_u32(out, bits);
}

void put_command(std::vector<uint8_t>& out, const Command& c) {
  put_u32(out, c.id);
  for (float v : c.rect) put_f32(out, v);
  for (float v : c.proj) put_f32(out, v);
  for (float v : c.color) put_f32(out, v);
}

Command pixel_rect(uint32_t w, uint32_t h, uint32_t id) {
  Command c;
  c.id = id;
  c.rect[0] = 0.0f;
  c.rect[1] = 0.0f;
  c.rect[2] = static_cast<float>(w);
  c.rect[3] = static_cast<float>(h);
  c.proj[0] = 2.0f / static_cast<float>(w);
  c.proj[5] = -2.0f / static_cast<float>(h);
  c.proj[10] = 1.0f;
  c.proj[12] = -1.0f;
  c.proj[13] = 1.0f;
  c.proj[15] = 1.0f;
  c.color[0] = 1.0f;
  return c;
}

std::string write_mask_scene(const std::string& path, uint32_t w, uint32_t h,
                             const std::vector<uint8_t>& payload, uint32_t kind = 1,
                             bool trailing = false, uint32_t commandID = 1,
                             float colorScale = 1.0f) {
  std::vector<uint8_t> bytes;
  put_u32(bytes, 0x31464d4e);
  put_u32(bytes, w);
  put_u32(bytes, h);
  put_u32(bytes, 60);
  put_u32(bytes, 1);
  put_u32(bytes, 1);
  put_u32(bytes, 1);
  put_u32(bytes, 1);
  put_u32(bytes, w);
  put_u32(bytes, h);
  put_u32(bytes, kind);
  put_u32(bytes, 0xFFFFFFFFu); // opaque white, R is the low byte
  put_u32(bytes, 0x00000000u);
  put_u32(bytes, 0);
  put_u32(bytes, static_cast<uint32_t>(payload.size()));
  bytes.insert(bytes.end(), payload.begin(), payload.end());
  put_u32(bytes, 1);
  Command c = pixel_rect(w, h, commandID);
  c.color[0] = colorScale;
  put_command(bytes, c);
  if (trailing) bytes.push_back(0x5a);
  std::ofstream out(path, std::ios::binary);
  check(static_cast<bool>(out), "open fixture");
  out.write(reinterpret_cast<const char*>(bytes.data()), static_cast<std::streamsize>(bytes.size()));
  check(static_cast<bool>(out), "write fixture");
  return path;
}

Scene mask_scene(uint32_t w, uint32_t h, const std::vector<uint8_t>& a8, uint32_t radius = 0) {
  Scene s;
  s.width = w;
  s.height = h;
  s.fps_num = 60;
  s.fps_den = 1;
  Texture t;
  t.id = 1;
  t.width = w;
  t.height = h;
  t.kind = 1;
  t.fillRGBA = 0xFFFFFFFFu;
  t.outlineRGBA = 0xFF0000FFu;
  t.outlineRadiusPx = radius;
  t.payload = a8;
  s.textures.push_back(t);
  s.frames.push_back(Frame{});
  s.frames[0].commands.push_back(pixel_rect(w, h, 1));
  return s;
}

void test_widths_and_stride() {
  const uint32_t widths[] = {1, 3, 7, 8, 9, 31, 32, 33};
  for (uint32_t w : widths) {
    std::vector<uint8_t> mask(w);
    for (uint32_t x = 0; x < w; ++x) mask[x] = static_cast<uint8_t>((x * 37u + 11u) & 0xffu);
    for (int bits : {8, 4, 2, 1}) {
      const std::string path = "build/nico-mask-probe/renderer-selftest.nmf";
      write_mask_scene(path, w, 1, mask);
      const Scene loaded = nm::load_scene(path);
      nm::Renderer renderer(loaded, bits);
      const size_t stride = static_cast<size_t>(w) * 4u + 5u;
      std::vector<uint8_t> output(stride, 0xa5);
      for (uint32_t x = 0; x < w; ++x) {
        output[x * 4u + 0] = 0;
        output[x * 4u + 1] = 0;
        output[x * 4u + 2] = 0;
        output[x * 4u + 3] = 255;
      }
      renderer.draw(0, output.data(), static_cast<int>(stride));
      const unsigned levels = (1u << bits) - 1u;
      for (uint32_t x = 0; x < w; ++x) {
        const unsigned q = (unsigned(mask[x]) * levels + 127u) / 255u;
        const unsigned a = (q * 255u + levels / 2u) / levels;
        const uint8_t expected = static_cast<uint8_t>(a);
        for (unsigned c = 0; c < 3; ++c)
          check(output[x * 4u + c] == expected, "low-bit decoded pixel");
        check(output[x * 4u + 3] == 255, "opaque background alpha");
      }
      for (size_t i = static_cast<size_t>(w) * 4u; i < stride; ++i)
        check(output[i] == 0xa5, "stride guard");
      check(renderer.stats().raw_mask_bytes == w, "raw mask stat");
      check(renderer.stats().packed_mask_bytes == ((w * static_cast<unsigned>(bits) + 7u) / 8u), "packed mask stat");
      check(renderer.stats().cache_bytes >= renderer.stats().packed_mask_bytes + uint64_t(w) * 2u, "cache stat");
    }
  }
}

void test_alpha_scale_and_solid() {
  Scene s = mask_scene(1, 1, {128});
  s.textures[0].outlineRGBA = 0; // Isolate command alpha; outline has its own test.
  s.frames[0].commands[0].color[0] = 0.5f;
  nm::Renderer renderer(s, 8);
  uint8_t pixel[] = {10, 20, 30, 255};
  renderer.draw(0, pixel, 4);
  // 128/255 mask then 0.5 command alpha, white premultiplied over opaque bg.
  const uint8_t expected = static_cast<uint8_t>(std::lround((128.0 / 255.0) * 0.5 * 255.0 + 10.0 * (1.0 - (128.0 / 255.0) * 0.5)));
  check(pixel[0] == expected, "color.x alpha multiplication");
  check(pixel[3] == 255, "alpha over opaque background");

  Scene solid;
  solid.width = solid.height = 1;
  solid.fps_num = 60;
  solid.fps_den = 1;
  solid.frames.push_back(Frame{});
  Command c = pixel_rect(1, 1, 0);
  c.color[0] = 0.25f;
  c.color[1] = 0.5f;
  c.color[2] = 0.75f;
  c.color[3] = 0.5f;
  solid.frames[0].commands.push_back(c);
  nm::Renderer solidRenderer(solid, 4);
  uint8_t dst[] = {0, 0, 0, 255};
  solidRenderer.draw(0, dst, 4);
  check(dst[0] == 64 && dst[1] == 128 && dst[2] == 191 && dst[3] == 255, "solid id0");
}

void test_outline_and_fallback() {
  Scene s = mask_scene(3, 1, {0, 255, 0}, 1);
  s.textures[0].fillRGBA = 0xFF00FF00u;
  s.textures[0].outlineRGBA = 0xFF0000FFu;
  nm::Renderer r(s, 8);
  uint8_t out[] = {0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255};
  r.draw(0, out, 12);
  check(out[0] > 200 && out[4] < 10 && out[8] > 200, "disk outline and fill-over-outline");

  Scene fallback;
  fallback.width = 2; fallback.height = 1; fallback.fps_num = 60; fallback.fps_den = 1;
  Texture t; t.id = 7; t.width = 2; t.height = 1; t.kind = 0;
  t.payload = {255, 0, 0, 255, 0, 0, 255, 255};
  fallback.textures.push_back(t);
  fallback.frames.push_back(Frame{});
  Command c = pixel_rect(2, 1, 7); c.color[0] = 0.5f;
  fallback.frames[0].commands.push_back(c);
  nm::Renderer fr(fallback, 2);
  uint8_t pixels[] = {0, 0, 0, 255, 0, 0, 0, 255};
  fr.draw(0, pixels, 8);
  check(pixels[0] >= 127 && pixels[0] <= 128 && pixels[2] == 0, "RGBA fallback first pixel");
  check(pixels[4] == 0 && pixels[6] >= 127 && pixels[6] <= 128, "RGBA fallback second pixel");
  check(fr.stats().cache_bytes == t.payload.size(), "RGBA cache stat");
}

void test_rejections() {
  const std::string path = "build/nico-mask-probe/renderer-selftest.nmf";
  write_mask_scene(path, 1, 1, {255}, 1, true);
  bool rejected = false;
  try { (void)nm::load_scene(path); } catch (const std::exception&) { rejected = true; }
  check(rejected, "trailing bytes rejection");
  write_mask_scene(path, 1, 1, {255}, 1, false, 99);
  rejected = false;
  try { (void)nm::load_scene(path); } catch (const std::exception&) { rejected = true; }
  check(rejected, "unknown ID rejection");
  write_mask_scene(path, 0, 1, {});
  rejected = false;
  try { (void)nm::load_scene(path); } catch (const std::exception&) { rejected = true; }
  check(rejected, "zero output width rejection");

  Scene rotated = mask_scene(2, 2, {255, 0, 0, 0});
  rotated.frames[0].commands[0].proj[0] = 0.0f;
  rotated.frames[0].commands[0].proj[1] = 1.0f;
  rotated.frames[0].commands[0].proj[4] = -1.0f;
  rotated.frames[0].commands[0].proj[5] = 0.0f;
  rejected = false;
  try { nm::Renderer bad(rotated, 8); } catch (const std::exception&) { rejected = true; }
  check(rejected, "rotation rejection");

  Scene hugeRadius = mask_scene(1, 1, {255}, 65);
  rejected = false;
  try { nm::Renderer bad(hugeRadius, 8); } catch (const std::exception&) { rejected = true; }
  check(rejected, "outline radius bound");
}

}  // namespace

int main() {
  const std::string fixture = "build/nico-mask-probe/renderer-selftest.nmf";
  try {
    test_widths_and_stride();
    test_alpha_scale_and_solid();
    test_outline_and_fallback();
    test_rejections();
    std::remove(fixture.c_str());
    std::cout << "renderer-selftest: PASS\n";
    return 0;
  } catch (const std::exception& e) {
    std::remove(fixture.c_str());
    std::cerr << e.what() << '\n';
    return 1;
  }
}
