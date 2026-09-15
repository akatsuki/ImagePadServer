if(NOT DEFINED BRIDGE OR NOT DEFINED GSTREAMER_RUNTIME_ROOT OR NOT DEFINED TEST_DIR)
  message(FATAL_ERROR "fixed-port CLI test configuration is incomplete")
endif()

file(MAKE_DIRECTORY "${TEST_DIR}")
set(ready_file "${TEST_DIR}/ready.json")
set(media_ready_file "${TEST_DIR}/media.ready")
set(event_log_file "${TEST_DIR}/events.jsonl")
set(recording_file "${TEST_DIR}/source-clock-録画-📱.mp4")
set(stop_file "${TEST_DIR}/source-clock-fixed-port-stop.request")
file(REMOVE "${ready_file}" "${media_ready_file}" "${event_log_file}" "${recording_file}" "${stop_file}")
file(WRITE "${stop_file}" "")

execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    "${BRIDGE}"
    --source-clock
    --video-listen-port 41001
    --audio-listen-port 41002
    --session-token 00112233445566778899aabbccddeeff
    --session-id fixed-port-test
    --publisher-generation 1
    --ready-file "${ready_file}"
    --media-ready-file "${media_ready_file}"
    --event-log "${event_log_file}"
    --no-signal-seconds 0
    --publish-url rtsp://127.0.0.1:1/test
    --recording "${recording_file}"
    --stop-file "${stop_file}"
    --width 320
    --height 180
    --source-fps 60
    --fps 30
    --bitrate 1000
    --maxrate 1200
    --buffer-size 2000
    --audio-bitrate 128000
    --gop 30
  RESULT_VARIABLE bridge_result
  OUTPUT_VARIABLE bridge_stdout
  ERROR_VARIABLE bridge_stderr
  TIMEOUT 15)

if(bridge_stderr MATCHES "usage: airplay-gstreamer-bridge")
  message(FATAL_ERROR "source-clock fixed ports were rejected by the CLI:\n${bridge_stderr}")
endif()
if(bridge_result MATCHES "timeout")
  message(FATAL_ERROR "source-clock fixed-port CLI test timed out")
endif()
if(bridge_result EQUAL 2)
  message(FATAL_ERROR "source-clock fixed ports were not accepted or could not be bound:\n${bridge_stderr}")
endif()
if(NOT bridge_result EQUAL 0)
  message(FATAL_ERROR "source-clock fixed-port stop exit=${bridge_result}, want 0:\n${bridge_stderr}")
endif()
if(NOT EXISTS "${ready_file}")
  message(FATAL_ERROR "source-clock CLI did not write publisher-ready JSON:\n${bridge_stderr}")
endif()
file(READ "${ready_file}" ready_json)
if(NOT ready_json MATCHES "\"schema\":2" OR
   NOT ready_json MATCHES "\"sessionId\":\"fixed-port-test\"" OR
   NOT ready_json MATCHES "\"publisherGeneration\":1" OR
   NOT ready_json MATCHES "\"pipelineStartAccepted\":true")
  message(FATAL_ERROR "source-clock publisher-ready JSON is incompatible: ${ready_json}")
endif()
if(NOT EXISTS "${recording_file}")
  message(FATAL_ERROR "source-clock CLI did not create the Unicode recording path:\n${bridge_stderr}")
endif()
file(REMOVE "${ready_file}" "${media_ready_file}" "${event_log_file}" "${recording_file}" "${stop_file}")
if(EXISTS "${recording_file}")
  message(FATAL_ERROR "source-clock CLI test did not clean up the Unicode recording")
endif()
