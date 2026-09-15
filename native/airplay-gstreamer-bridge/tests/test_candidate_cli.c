/* Execute the production parser; intercept only process startup (no sockets,
 * decoder, paths or environment changes). Assert the exact forwarded tuple. */
#include <stdbool.h>
#define airplay_pipeline_run fake_legacy_run
#define airplay_direct_pipeline_run fake_direct_run
#define airplay_source_clock_pipeline_run fake_source_run
#define wmain embedded_wmain
#define main embedded_main
#include "../main.c"
#undef main
#undef wmain
#include "test_check.h"

static unsigned calls;
static uint32_t forwarded_generation, forwarded_watermark;
int fake_legacy_run(int v, int a) { (void)v; (void)a; ++calls; return 77; }
int fake_direct_run(int v, int a, const char *u, const char *r, const char *s,
    int w, int h, int f, int b, int m, int z, int ab, int g) {
  (void)v;(void)a;(void)u;(void)r;(void)s;(void)w;(void)h;(void)f;(void)b;(void)m;(void)z;(void)ab;(void)g;
  ++calls; return 77;
}
int fake_source_run(const char *u, const char *r, const char *s, const char *token,
    const char *id, uint64_t pub, const char *ready, const char *media, const char *log,
    int v, int a, int w, int h, int sf, int f, int b, int m, int z, int ab, int g, int no_signal,
    uint32_t proof_generation, uint32_t proof_watermark) {
  (void)u;(void)r;(void)s;(void)token;(void)id;(void)pub;(void)ready;(void)media;(void)log;
  (void)v;(void)a;(void)w;(void)h;(void)sf;(void)f;(void)b;(void)m;(void)z;(void)ab;(void)g;(void)no_signal;
  forwarded_generation = proof_generation;
  forwarded_watermark = proof_watermark;
  ++calls; return 77;
}
static void parse_case(const char *generation, const char *watermark, bool valid) {
  char *args[40] = {"bridge", "--source-clock", "--session-token", "test-token",
    "--session-id", "test-session", "--publisher-generation", "2",
    "--ready-file", "ready", "--media-ready-file", "media", "--event-log", "events",
    "--publish-url", "rtsp://127.0.0.1:1/test", "--recording", "record", "--stop-file", "stop"};
  int n = 20;
  if (generation != NULL) { args[n++] = "--proof-source-generation"; args[n++] = (char *)generation; }
  if (watermark != NULL) { args[n++] = "--proof-video-watermark"; args[n++] = (char *)watermark; }
  calls = 0; forwarded_generation = forwarded_watermark = 0;
  TEST_CHECK(run_main(n, args) == (valid ? 77 : 2));
  TEST_CHECK(calls == (valid ? 1u : 0u));
  if (valid) {
    TEST_CHECK(forwarded_generation == (generation == NULL ? 0 : (uint32_t)strtoull(generation, NULL, 10)));
    TEST_CHECK(forwarded_watermark == (watermark == NULL ? 0 : (uint32_t)strtoull(watermark, NULL, 10)));
  }
}
int main(void) {
  parse_case(NULL, NULL, true); /* legacy source-clock path */
  parse_case("7", "100", true);
  parse_case("4294967295", "4294967295", true);
  parse_case("7", NULL, false); parse_case(NULL, "100", false);
  parse_case("0", "100", false); parse_case("7", "0", false);
  parse_case("+7", "100", false); parse_case("7", "-1", false);
  parse_case("7x", "100", false); parse_case("7", "100x", false);
  parse_case("4294967296", "100", false); parse_case("7", "4294967296", false);
  puts("candidate CLI tests: PASS");
  return 0;
}
