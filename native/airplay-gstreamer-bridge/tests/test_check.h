#ifndef IMAGEPAD_TEST_CHECK_H
#define IMAGEPAD_TEST_CHECK_H

#include <stdio.h>
#include <stdlib.h>

#define TEST_CHECK(expr) do { \
  if (!(expr)) { \
    fprintf(stderr, "TEST_CHECK_FAILED: %s (%s:%d)\n", #expr, __FILE__, __LINE__); \
    exit(1); \
  } \
} while (0)

#endif
