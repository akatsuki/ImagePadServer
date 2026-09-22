// Diagnostic-only NPS3 compositor stub.
// It passes the ABI probe and then fails at runtime when the scene is sent.
#include <cstdio>
#include <cstring>

int main(int argc, char** argv) {
    const char* version = "NICO_COMPOSITOR 1 NPS3 WARP";
    if (argc == 2 && std::strcmp(argv[1], "--self-test") == 0) {
        std::puts(version);
        return 0;
    }
    if (argc == 2 && std::strcmp(argv[1], "--stdin") == 0) {
        std::fputs("diagnostic compositor runtime failure\n", stderr);
        return 42;
    }
    std::fputs("--self-test or --stdin required\n", stderr);
    return 2;
}
