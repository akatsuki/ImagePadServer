if(NOT DEFINED PROBE OR NOT DEFINED GSTREAMER_RUNTIME_ROOT)
  message(FATAL_ERROR "capability probe test configuration is incomplete")
endif()

execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    "${PROBE}"
  RESULT_VARIABLE probe_result
  OUTPUT_VARIABLE probe_stdout
  ERROR_VARIABLE probe_stderr)

if(NOT probe_result EQUAL 0)
  message(FATAL_ERROR
    "capability probe exited ${probe_result}\n"
    "stdout:\n${probe_stdout}\n"
    "stderr:\n${probe_stderr}")
endif()

string(JSON root_type ERROR_VARIABLE json_error TYPE "${probe_stdout}")
if(json_error)
  message(FATAL_ERROR
    "capability probe stdout is not valid JSON: ${json_error}\n"
    "stdout:\n${probe_stdout}\n"
    "stderr:\n${probe_stderr}")
endif()
string(JSON schema_type TYPE "${probe_stdout}" schema)
string(JSON schema GET "${probe_stdout}" schema)
if(NOT schema_type STREQUAL "NUMBER" OR NOT schema EQUAL 1)
  message(FATAL_ERROR "capability probe schema must be numeric 1: ${probe_stdout}")
endif()

string(JSON element_count LENGTH "${probe_stdout}" elements)
if(element_count LESS 1)
  message(FATAL_ERROR "capability probe returned no element records: ${probe_stdout}")
endif()
string(JSON missing GET "${probe_stdout}" missing)
if(NOT missing EQUAL 0)
  message(FATAL_ERROR "capability probe reports missing capabilities: ${probe_stdout}")
endif()

foreach(field videoBuffer videoEOS audioBuffer audioEOS)
  string(JSON value GET "${probe_stdout}" sourceClock ${field})
  if(NOT value)
    message(FATAL_ERROR "sourceClock.${field} is not true: ${probe_stdout}")
  endif()
endforeach()
