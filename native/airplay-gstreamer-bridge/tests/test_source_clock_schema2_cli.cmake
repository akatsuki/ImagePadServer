if(NOT DEFINED BRIDGE OR NOT DEFINED GSTREAMER_RUNTIME_ROOT OR NOT DEFINED TEST_DIR)
  message(FATAL_ERROR "schema-2 CLI test configuration is incomplete")
endif()

file(MAKE_DIRECTORY "${TEST_DIR}")

function(expect_schema2_cli_rejected label session_value generation_value event_log_value)
  set(args
    --source-clock
    --video-listen-port 0
    --audio-listen-port 0
    --session-token 00112233445566778899aabbccddeeff
    --ready-file "${TEST_DIR}/${label}.ready.json"
    --media-ready-file "${TEST_DIR}/${label}.media.json"
    --no-signal-seconds 1
    --publish-url rtsp://127.0.0.1:1/test
    --recording "${TEST_DIR}/${label}.mp4"
    --stop-file "${TEST_DIR}/${label}.stop"
    --width 320
    --height 180
    --source-fps 60
    --fps 30
    --bitrate 1000
    --maxrate 1200
    --buffer-size 2000
    --audio-bitrate 128000
    --gop 30)
  if(NOT session_value STREQUAL "<missing>")
    list(APPEND args --session-id "${session_value}")
  endif()
  if(NOT generation_value STREQUAL "<missing>")
    list(APPEND args --publisher-generation "${generation_value}")
  endif()
  if(NOT event_log_value STREQUAL "<missing>")
    list(APPEND args --event-log "${event_log_value}")
  endif()

  execute_process(
    COMMAND "${CMAKE_COMMAND}" -E env
      "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
      "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
      "GST_PLUGIN_SYSTEM_PATH_1_0="
      "${BRIDGE}" ${args}
    RESULT_VARIABLE result
    OUTPUT_VARIABLE stdout
    ERROR_VARIABLE stderr
    TIMEOUT 5)
  if(result MATCHES "timeout")
    message(FATAL_ERROR "${label}: invalid schema-2 CLI reached runtime and timed out")
  endif()
  if(NOT result EQUAL 2)
    message(FATAL_ERROR "${label}: exit ${result}, want 2\n${stderr}")
  endif()
endfunction()

expect_schema2_cli_rejected(missing-session "<missing>" 1 "${TEST_DIR}/missing-session.events.jsonl")
expect_schema2_cli_rejected(missing-generation valid-session "<missing>" "${TEST_DIR}/missing-generation.events.jsonl")
expect_schema2_cli_rejected(zero-generation valid-session 0 "${TEST_DIR}/zero-generation.events.jsonl")
expect_schema2_cli_rejected(invalid-generation valid-session 1x "${TEST_DIR}/invalid-generation.events.jsonl")
expect_schema2_cli_rejected(signed-generation valid-session " -1" "${TEST_DIR}/signed-generation.events.jsonl")
expect_schema2_cli_rejected(plus-generation valid-session +1 "${TEST_DIR}/plus-generation.events.jsonl")
expect_schema2_cli_rejected(missing-event-log valid-session 1 "<missing>")
