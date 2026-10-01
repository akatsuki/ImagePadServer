#define WIN32_LEAN_AND_MEAN
#define NOMINMAX

#include <windows.h>
#include <d3d11.h>
#include <dxgi.h>
#include <nvEncodeAPI.h>

#include "frame_pool.h"

#include <algorithm>
#include <cstdint>
#include <fstream>
#include <iomanip>
#include <sstream>
#include <string>

namespace {

constexpr UINT kWidth = 128;
constexpr UINT kHeight = 72;
constexpr std::size_t kPoolCapacity = 4;
constexpr std::size_t kPoolTurnsBeforeEos = 4;

std::string json_escape(const std::string& value) {
    std::ostringstream escaped;
    for (unsigned char character : value) {
        switch (character) {
        case '\\': escaped << "\\\\"; break;
        case '"': escaped << "\\\""; break;
        case '\n': escaped << "\\n"; break;
        case '\r': escaped << "\\r"; break;
        case '\t': escaped << "\\t"; break;
        default:
            if (character < 0x20) {
                escaped << "\\u" << std::hex << std::setw(4) << std::setfill('0')
                        << static_cast<unsigned int>(character) << std::dec;
            } else {
                escaped << character;
            }
        }
    }
    return escaped.str();
}

std::string status_message(NVENCSTATUS status) {
    std::ostringstream message;
    message << "nvenc_status_" << static_cast<unsigned int>(status);
    return message.str();
}

void write_result(
    const std::string& output,
    const std::string& status,
    const std::string& reason,
    bool texture_registration,
    bool nvenc_acceptance,
    bool pool_progress,
    std::size_t pool_capacity,
    std::size_t submitted_before_eos,
    std::size_t accepted_frames,
    std::size_t texture_registrations,
    bool eos_sent,
    bool eos_completed,
    const std::string& adapter_name = "") {
    const bool pool_overflow = eos_sent && pool_capacity > 0 &&
        submitted_before_eos >= pool_capacity * kPoolTurnsBeforeEos;
    std::ofstream file(output, std::ios::binary | std::ios::trunc);
    file << "{\n"
         << "  \"schema_version\": 1,\n"
         << "  \"experiment\": \"nico-gpu-nvenc\",\n"
         << "  \"status\": \"" << json_escape(status) << "\",\n"
         << "  \"reason\": " << (reason.empty() ? "null" : "\"" + json_escape(reason) + "\"") << ",\n"
         << "  \"unavailable_reasons\": [],\n"
         << "  \"checks\": {\n"
         << "    \"d3d11_texture_registration\": " << (texture_registration ? "true" : "false") << ",\n"
         << "    \"nvenc_acceptance\": " << (nvenc_acceptance ? "true" : "false") << ",\n"
         << "    \"pool_progress\": " << (pool_progress ? "true" : "false") << "\n"
         << "  },\n"
         << "  \"gates\": {\n"
         << "    \"d3d11_texture_registration\": " << (texture_registration ? "true" : "false") << ",\n"
         << "    \"nvenc_acceptance\": " << (nvenc_acceptance ? "true" : "false") << ",\n"
         << "    \"pool_progress\": " << (pool_progress ? "true" : "false") << ",\n"
         << "    \"pool_capacity_exceeded_before_eos\": " << (pool_overflow ? "true" : "false") << "\n"
         << "  },\n"
         << "  \"counters\": {\n"
         << "    \"pool_capacity\": " << pool_capacity << ",\n"
         << "    \"submitted_before_eos\": " << submitted_before_eos << ",\n"
         << "    \"accepted_frames\": " << accepted_frames << ",\n"
         << "    \"texture_registrations\": " << texture_registrations << ",\n"
         << "    \"eos_sent\": " << (eos_sent ? "true" : "false") << ",\n"
         << "    \"eos_completed\": " << (eos_completed ? "true" : "false") << "\n"
         << "  },\n"
         << "  \"toolchain\": {\n"
         << "    \"nvenc_input\": \"d3d11_texture_nv12\",\n"
         << "    \"compositor\": \"not_run\",\n"
         << "    \"readback_bytes\": 0,\n"
         << "    \"pipe_image_bytes\": 0,\n"
         << "    \"background_upload_bytes\": 0,\n"
         << "    \"gpu_internal_copy_bytes\": 0\n"
         << "  },\n"
         << "  \"commands\": [],\n"
         << "  \"metadata\": {\n"
         << "    \"adapter\": \"" << json_escape(adapter_name) << "\",\n"
         << "    \"width\": " << kWidth << ",\n"
         << "    \"height\": " << kHeight << ",\n"
         << "    \"pool_turns_before_eos\": " << kPoolTurnsBeforeEos << "\n"
         << "  }\n"
         << "}\n";
}

int fail(const std::string& output, const std::string& reason, std::size_t registered = 0,
         std::size_t submitted = 0, std::size_t accepted = 0, bool eos_sent = false,
         bool eos_completed = false, const std::string& adapter = "") {
    write_result(output, "failed", reason, registered > 0, accepted > 0, false,
                 kPoolCapacity, submitted, accepted, registered, eos_sent, eos_completed, adapter);
    return 1;
}

} // namespace

int main(int argc, char** argv) {
    std::string output;
    for (int index = 1; index + 1 < argc; ++index) {
        if (std::string(argv[index]) == "--output") {
            output = argv[index + 1];
            ++index;
        }
    }
    if (output.empty()) {
        return 2;
    }

    HMODULE nvenc_dll = LoadLibraryW(L"NvEncodeAPI64.dll");
    if (nvenc_dll == nullptr) {
        return fail(output, "nvenc_runtime_load_failed");
    }
    auto create_instance = reinterpret_cast<NVENCSTATUS(NVENCAPI*)(NV_ENCODE_API_FUNCTION_LIST*)>(
        GetProcAddress(nvenc_dll, "NvEncodeAPICreateInstance"));
    if (create_instance == nullptr) {
        FreeLibrary(nvenc_dll);
        return fail(output, "nvenc_create_instance_missing");
    }

    NV_ENCODE_API_FUNCTION_LIST api{};
    api.version = NV_ENCODE_API_FUNCTION_LIST_VER;
    NVENCSTATUS status = create_instance(&api);
    if (status != NV_ENC_SUCCESS) {
        FreeLibrary(nvenc_dll);
        return fail(output, status_message(status));
    }

    D3D_FEATURE_LEVEL feature_level = D3D_FEATURE_LEVEL_11_0;
    Microsoft::WRL::ComPtr<ID3D11Device> device;
    Microsoft::WRL::ComPtr<ID3D11DeviceContext> context;
    HRESULT d3d_result = D3D11CreateDevice(
        nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr, 0, &feature_level, 1,
        D3D11_SDK_VERSION, &device, &feature_level, &context);
    if (FAILED(d3d_result)) {
        FreeLibrary(nvenc_dll);
        return fail(output, "d3d11_hardware_device_create_failed");
    }

    Microsoft::WRL::ComPtr<IDXGIDevice> dxgi_device;
    std::string adapter_name;
    if (SUCCEEDED(device.As(&dxgi_device))) {
        Microsoft::WRL::ComPtr<IDXGIAdapter> adapter;
        if (SUCCEEDED(dxgi_device->GetAdapter(&adapter))) {
            DXGI_ADAPTER_DESC description{};
            if (SUCCEEDED(adapter->GetDesc(&description))) {
                char converted[256]{};
                WideCharToMultiByte(CP_UTF8, 0, description.Description, -1, converted,
                                    static_cast<int>(sizeof(converted)), nullptr, nullptr);
                adapter_name = converted;
            }
        }
    }

    NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS open{};
    open.version = NV_ENC_OPEN_ENCODE_SESSION_EX_PARAMS_VER;
    open.device = device.Get();
    open.deviceType = NV_ENC_DEVICE_TYPE_DIRECTX;
    open.apiVersion = NVENCAPI_VERSION;
    void* encoder = nullptr;
    status = api.nvEncOpenEncodeSessionEx(&open, &encoder);
    if (status != NV_ENC_SUCCESS) {
        FreeLibrary(nvenc_dll);
        return fail(output, "nvenc_session_open_failed_" + status_message(status), 0, 0, 0, false, false, adapter_name);
    }

    NV_ENC_INITIALIZE_PARAMS initialize{};
    initialize.version = NV_ENC_INITIALIZE_PARAMS_VER;
    initialize.encodeGUID = NV_ENC_CODEC_H264_GUID;
    initialize.presetGUID = NV_ENC_PRESET_P4_GUID;
    initialize.encodeWidth = kWidth;
    initialize.encodeHeight = kHeight;
    initialize.darWidth = kWidth;
    initialize.darHeight = kHeight;
    initialize.frameRateNum = 30;
    initialize.frameRateDen = 1;
    initialize.enablePTD = 1;
    initialize.enableEncodeAsync = 0;
    status = api.nvEncInitializeEncoder(encoder, &initialize);
    if (status != NV_ENC_SUCCESS) {
        api.nvEncDestroyEncoder(encoder);
        FreeLibrary(nvenc_dll);
        return fail(output, "nvenc_initialize_failed_" + status_message(status), 0, 0, 0, false, false, adapter_name);
    }

    FramePool pool;
    std::string pool_error;
    if (!pool.create(device.Get(), kWidth, kHeight, kPoolCapacity, pool_error)) {
        api.nvEncDestroyEncoder(encoder);
        FreeLibrary(nvenc_dll);
        return fail(output, pool_error, 0, 0, 0, false, false, adapter_name);
    }

    std::size_t registered = 0;
    for (std::size_t index = 0; index < pool.capacity(); ++index) {
        NV_ENC_REGISTER_RESOURCE registration{};
        registration.version = NV_ENC_REGISTER_RESOURCE_VER;
        registration.resourceType = NV_ENC_INPUT_RESOURCE_TYPE_DIRECTX;
        registration.resourceToRegister = pool.at(index).texture.Get();
        registration.width = kWidth;
        registration.height = kHeight;
        registration.bufferFormat = NV_ENC_BUFFER_FORMAT_NV12;
        registration.bufferUsage = NV_ENC_INPUT_IMAGE;
        status = api.nvEncRegisterResource(encoder, &registration);
        if (status != NV_ENC_SUCCESS) {
            api.nvEncDestroyEncoder(encoder);
            FreeLibrary(nvenc_dll);
            return fail(output, "d3d11_texture_registration_failed_" + status_message(status), registered, 0, 0, false, false, adapter_name);
        }
        pool.at(index).registered_resource = registration.registeredResource;
        ++registered;
    }

    NV_ENC_CREATE_BITSTREAM_BUFFER bitstream{};
    bitstream.version = NV_ENC_CREATE_BITSTREAM_BUFFER_VER;
    status = api.nvEncCreateBitstreamBuffer(encoder, &bitstream);
    if (status != NV_ENC_SUCCESS) {
        api.nvEncDestroyEncoder(encoder);
        FreeLibrary(nvenc_dll);
        return fail(output, "bitstream_buffer_create_failed_" + status_message(status), registered, 0, 0, false, false, adapter_name);
    }

    std::size_t submitted = 0;
    std::size_t accepted = 0;
    const std::size_t target = pool.capacity() * kPoolTurnsBeforeEos;
    for (std::size_t index = 0; index < target; ++index) {
        FrameSlot& slot = pool.at(index);
        NV_ENC_MAP_INPUT_RESOURCE map{};
        map.version = NV_ENC_MAP_INPUT_RESOURCE_VER;
        map.registeredResource = slot.registered_resource;
        status = api.nvEncMapInputResource(encoder, &map);
        if (status != NV_ENC_SUCCESS) {
            api.nvEncDestroyEncoder(encoder);
            FreeLibrary(nvenc_dll);
            return fail(output, "nvenc_map_input_failed_" + status_message(status), registered, submitted, accepted, false, false, adapter_name);
        }

        NV_ENC_PIC_PARAMS picture{};
        picture.version = NV_ENC_PIC_PARAMS_VER;
        picture.inputBuffer = map.mappedResource;
        picture.bufferFmt = NV_ENC_BUFFER_FORMAT_NV12;
        picture.inputWidth = kWidth;
        picture.inputHeight = kHeight;
        picture.outputBitstream = bitstream.bitstreamBuffer;
        picture.pictureStruct = NV_ENC_PIC_STRUCT_FRAME;
        picture.inputTimeStamp = static_cast<uint64_t>(index);
        ++submitted;
        status = api.nvEncEncodePicture(encoder, &picture);
        api.nvEncUnmapInputResource(encoder, map.mappedResource);
        if (status != NV_ENC_SUCCESS) {
            api.nvEncDestroyEncoder(encoder);
            FreeLibrary(nvenc_dll);
            return fail(output, "nvenc_acceptance_failed_" + status_message(status), registered, submitted, accepted, false, false, adapter_name);
        }

        NV_ENC_LOCK_BITSTREAM lock{};
        lock.version = NV_ENC_LOCK_BITSTREAM_VER;
        lock.outputBitstream = bitstream.bitstreamBuffer;
        lock.doNotWait = 0;
        status = api.nvEncLockBitstream(encoder, &lock);
        if (status != NV_ENC_SUCCESS) {
            api.nvEncDestroyEncoder(encoder);
            FreeLibrary(nvenc_dll);
            return fail(output, "nvenc_output_progress_failed_" + status_message(status), registered, submitted, accepted, false, false, adapter_name);
        }
        api.nvEncUnlockBitstream(encoder, bitstream.bitstreamBuffer);
        ++accepted;
    }

    NV_ENC_PIC_PARAMS eos{};
    eos.version = NV_ENC_PIC_PARAMS_VER;
    eos.encodePicFlags = NV_ENC_PIC_FLAG_EOS;
    const bool eos_sent = api.nvEncEncodePicture(encoder, &eos) == NV_ENC_SUCCESS;
    const bool eos_completed = eos_sent;
    api.nvEncDestroyBitstreamBuffer(encoder, bitstream.bitstreamBuffer);
    for (std::size_t index = 0; index < pool.capacity(); ++index) {
        if (pool.at(index).registered_resource != nullptr) {
            api.nvEncUnregisterResource(encoder, pool.at(index).registered_resource);
        }
    }
    api.nvEncDestroyEncoder(encoder);
    FreeLibrary(nvenc_dll);

    if (!eos_sent) {
        return fail(output, "nvenc_eos_failed", registered, submitted, accepted, true, false, adapter_name);
    }
    write_result(output, "passed", "", registered == kPoolCapacity, accepted == target,
                 submitted >= target, pool.capacity(), submitted, accepted, registered,
                 eos_sent, eos_completed, adapter_name);
    return 0;
}
