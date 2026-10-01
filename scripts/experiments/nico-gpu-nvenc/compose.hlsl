// T3 deliberately does not implement the production compositor.
// This shader is reserved for a later isolated experiment and is not loaded by
// the registration/acceptance self-test.
Texture2D<float4> InputTexture : register(t0);
RWTexture2D<float4> OutputTexture : register(u0);

[numthreads(8, 8, 1)]
void CopyTexture(uint3 dispatchId : SV_DispatchThreadID) {
    OutputTexture[dispatchId.xy] = InputTexture.Load(int3(dispatchId.xy, 0));
}
