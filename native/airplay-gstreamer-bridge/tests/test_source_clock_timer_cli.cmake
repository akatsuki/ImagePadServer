if(NOT DEFINED BRIDGE OR NOT DEFINED GSTREAMER_RUNTIME_ROOT OR NOT DEFINED TEST_DIR)
  message(FATAL_ERROR "source-clock timer CLI test configuration is incomplete")
endif()

file(MAKE_DIRECTORY "${TEST_DIR}")

set(common_args
  --source-clock
  --video-listen-port 0
  --audio-listen-port 0
  --session-token 00112233445566778899aabbccddeeff
  --session-id timer-cli-test
  --publisher-generation 1
  --ready-file "${TEST_DIR}/ready.json"
  --media-ready-file "${TEST_DIR}/media.ready"
  --event-log "${TEST_DIR}/events.jsonl"
  --publish-url rtsp://127.0.0.1:1/test
  --recording "${TEST_DIR}/recording.mp4"
  --stop-file "${TEST_DIR}/stop.request"
  --width 320
  --height 180
  --source-fps 60
  --fps 30
  --bitrate 1000
  --maxrate 1200
  --buffer-size 2000
  --audio-bitrate 128000
  --gop 30)

execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    "${BRIDGE}" ${common_args} --no-signal-seconds -1
  RESULT_VARIABLE negative_result
  OUTPUT_VARIABLE negative_stdout
  ERROR_VARIABLE negative_stderr
  TIMEOUT 5)
if(NOT negative_result EQUAL 2 OR
   NOT negative_stderr MATCHES "usage: airplay-gstreamer-bridge")
  message(FATAL_ERROR
    "negative --no-signal-seconds must be usage+exit 2, got ${negative_result}:\n${negative_stderr}")
endif()

set(zero_stop_file "${TEST_DIR}/zero.stop.request")
set(zero_stdout_file "${TEST_DIR}/zero.stdout")
set(zero_stderr_file "${TEST_DIR}/zero.stderr")
file(REMOVE "${zero_stop_file}" "${zero_stdout_file}" "${zero_stderr_file}")

set(zero_args ${common_args} --stop-file "${zero_stop_file}" --no-signal-seconds 0)
set(zero_ps_args "")
set(zero_ps_separator "")
foreach(arg IN LISTS zero_args)
  string(REPLACE "'" "''" arg_for_ps "${arg}")
  string(APPEND zero_ps_args "${zero_ps_separator}'${arg_for_ps}'")
  set(zero_ps_separator ",")
endforeach()
string(REPLACE "'" "''" bridge_for_ps "${BRIDGE}")
string(REPLACE "'" "''" stop_for_ps "${zero_stop_file}")
string(REPLACE "'" "''" stdout_for_ps "${zero_stdout_file}")
string(REPLACE "'" "''" stderr_for_ps "${zero_stderr_file}")
set(zero_ps_code
  "$ErrorActionPreference='Stop'; $bridge='${bridge_for_ps}'; $bridge_args=@(${zero_ps_args}); $arg_line=(($bridge_args | ForEach-Object { '\"' + $_ + '\"' }) -join ' '); $process=Start-Process -FilePath $bridge -ArgumentList $arg_line -RedirectStandardOutput '${stdout_for_ps}' -RedirectStandardError '${stderr_for_ps}' -WindowStyle Hidden -PassThru; Start-Sleep -Milliseconds 1500; if($process.HasExited){ exit 98 }; [System.IO.File]::WriteAllText('${stop_for_ps}',''); if(-not $process.WaitForExit(10000)){ try { $process.Kill() } catch {} ; exit 99 }; exit $process.ExitCode")

if(DEFINED ENV{SystemRoot})
  set(powershell_executable "$ENV{SystemRoot}/System32/WindowsPowerShell/v1.0/powershell.exe")
else()
  set(powershell_executable powershell.exe)
endif()
execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    "${powershell_executable}" -NoProfile -ExecutionPolicy Bypass -Command "${zero_ps_code}"
  RESULT_VARIABLE zero_result
  OUTPUT_VARIABLE zero_wrapper_stdout
  ERROR_VARIABLE zero_wrapper_stderr
  TIMEOUT 20)

if(zero_result MATCHES "timeout")
  message(FATAL_ERROR "zero --no-signal-seconds test timed out")
endif()
if(NOT zero_result EQUAL 0)
  set(zero_stderr "<missing>")
  if(EXISTS "${zero_stderr_file}")
    file(READ "${zero_stderr_file}" zero_stderr)
  endif()
  message(FATAL_ERROR
    "zero --no-signal-seconds must remain alive past one second and stop with 0, got ${zero_result}:\nwrapper stdout=${zero_wrapper_stdout}\nwrapper stderr=${zero_wrapper_stderr}\nchild stderr=${zero_stderr}")
endif()
if(NOT EXISTS "${zero_stop_file}")
  message(FATAL_ERROR "zero --no-signal-seconds test did not reach its stop-file checkpoint")
endif()

set(positive_stop_file "${TEST_DIR}/positive.stop.request")
set(positive_stdout_file "${TEST_DIR}/positive.stdout")
set(positive_stderr_file "${TEST_DIR}/positive.stderr")
file(REMOVE
  "${TEST_DIR}/ready.json"
  "${TEST_DIR}/media.ready"
  "${TEST_DIR}/events.jsonl"
  "${TEST_DIR}/recording.mp4"
  "${positive_stop_file}"
  "${positive_stdout_file}"
  "${positive_stderr_file}")
string(REPLACE "${zero_stop_file}" "${positive_stop_file}" positive_ps_code "${zero_ps_code}")
string(REPLACE "${zero_stdout_file}" "${positive_stdout_file}" positive_ps_code "${positive_ps_code}")
string(REPLACE "${zero_stderr_file}" "${positive_stderr_file}" positive_ps_code "${positive_ps_code}")
string(REPLACE "'--no-signal-seconds','0'" "'--no-signal-seconds','1'" positive_ps_code "${positive_ps_code}")

execute_process(
  COMMAND "${CMAKE_COMMAND}" -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    "${powershell_executable}" -NoProfile -ExecutionPolicy Bypass -Command "${positive_ps_code}"
  RESULT_VARIABLE positive_result
  OUTPUT_VARIABLE positive_wrapper_stdout
  ERROR_VARIABLE positive_wrapper_stderr
  TIMEOUT 20)

if(positive_result MATCHES "timeout")
  message(FATAL_ERROR "positive pre-media --no-signal-seconds test timed out")
endif()
if(NOT positive_result EQUAL 0)
  set(positive_stderr "<missing>")
  if(EXISTS "${positive_stderr_file}")
    file(READ "${positive_stderr_file}" positive_stderr)
  endif()
  message(FATAL_ERROR
    "positive --no-signal-seconds started before decoded media, exit=${positive_result}:\nwrapper stdout=${positive_wrapper_stdout}\nwrapper stderr=${positive_wrapper_stderr}\nchild stderr=${positive_stderr}")
endif()
if(EXISTS "${TEST_DIR}/media.ready")
  message(FATAL_ERROR "positive pre-media timer test unexpectedly observed decoded media")
endif()

file(REMOVE
  "${TEST_DIR}/ready.json"
  "${TEST_DIR}/media.ready"
  "${TEST_DIR}/events.jsonl"
  "${TEST_DIR}/recording.mp4"
  "${TEST_DIR}/stop.request"
  "${zero_stop_file}"
  "${zero_stdout_file}"
  "${zero_stderr_file}"
  "${positive_stop_file}"
  "${positive_stdout_file}"
  "${positive_stderr_file}")
