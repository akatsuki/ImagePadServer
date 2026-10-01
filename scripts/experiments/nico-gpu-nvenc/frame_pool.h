#pragma once

#include <d3d11.h>
#include <wrl/client.h>

#include <cstddef>
#include <string>
#include <vector>

struct FrameSlot {
    Microsoft::WRL::ComPtr<ID3D11Texture2D> texture;
    void* registered_resource = nullptr;
};

class FramePool {
public:
    bool create(ID3D11Device* device, UINT width, UINT height, std::size_t capacity, std::string& error) {
        if (device == nullptr || capacity == 0) {
            error = "invalid_frame_pool_arguments";
            return false;
        }

        D3D11_TEXTURE2D_DESC description{};
        description.Width = width;
        description.Height = height;
        description.MipLevels = 1;
        description.ArraySize = 1;
        description.Format = DXGI_FORMAT_NV12;
        description.SampleDesc.Count = 1;
        description.Usage = D3D11_USAGE_DEFAULT;
        description.BindFlags = D3D11_BIND_SHADER_RESOURCE;

        slots_.resize(capacity);
        for (FrameSlot& slot : slots_) {
            HRESULT result = device->CreateTexture2D(&description, nullptr, &slot.texture);
            if (FAILED(result)) {
                error = "d3d11_texture_create_failed";
                slots_.clear();
                return false;
            }
        }
        return true;
    }

    std::size_t capacity() const { return slots_.size(); }

    FrameSlot& at(std::size_t index) { return slots_.at(index % slots_.size()); }

private:
    std::vector<FrameSlot> slots_;
};
