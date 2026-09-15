execute_process(
  COMMAND "${PROBE}"
  RESULT_VARIABLE probe_result
  OUTPUT_VARIABLE probe_stdout
  ERROR_VARIABLE probe_stderr
)

if(NOT probe_result STREQUAL "1")
  message(FATAL_ERROR
    "test_check_probe must exit exactly 1, got '${probe_result}'\n"
    "stdout:\n${probe_stdout}\n"
    "stderr:\n${probe_stderr}")
endif()

string(FIND "${probe_stderr}" "TEST_CHECK_FAILED" marker)
if(marker EQUAL -1)
  message(FATAL_ERROR
    "test_check_probe stderr must contain TEST_CHECK_FAILED\n"
    "stdout:\n${probe_stdout}\n"
    "stderr:\n${probe_stderr}")
endif()
