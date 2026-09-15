cmake_minimum_required(VERSION 3.20)
project(airplay_gstreamer_bridge C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

set(GSTREAMER_ROOT "" CACHE PATH "GStreamer MSVC x64 development root")
set(GSTREAMER_RUNTIME_ROOT "" CACHE PATH "GStreamer MSVC x64 runtime root")
if(NOT GSTREAMER_ROOT AND DEFINED ENV{GSTREAMER_1_0_ROOT_MSVC_X86_64})
  set(GSTREAMER_ROOT "$ENV{GSTREAMER_1_0_ROOT_MSVC_X86_64}")
endif()
if(NOT GSTREAMER_ROOT)
  set(GSTREAMER_ROOT "C:/gstreamer/1.0/msvc_x86_64")
endif()
if(NOT GSTREAMER_RUNTIME_ROOT)
  set(GSTREAMER_RUNTIME_ROOT "${GSTREAMER_ROOT}")
endif()

find_path(GSTREAMER_INCLUDE_DIR gst/gst.h
  PATHS "${GSTREAMER_ROOT}/include/gstreamer-1.0"
  NO_DEFAULT_PATH)
find_path(GLIB_INCLUDE_DIR glib.h
  PATHS "${GSTREAMER_ROOT}/include/glib-2.0"
  NO_DEFAULT_PATH)
find_path(GLIB_CONFIG_INCLUDE_DIR glibconfig.h
  PATHS "${GSTREAMER_ROOT}/lib/glib-2.0/include"
  NO_DEFAULT_PATH)
find_path(GSTAPP_INCLUDE_DIR gst/app/gstappsrc.h
  PATHS "${GSTREAMER_ROOT}/include/gstreamer-1.0"
  NO_DEFAULT_PATH)
find_library(GSTREAMER_LIBRARY NAMES gstreamer-1.0
  PATHS "${GSTREAMER_ROOT}/lib"
  NO_DEFAULT_PATH)
find_library(GLIB_LIBRARY NAMES glib-2.0
  PATHS "${GSTREAMER_ROOT}/lib"
  NO_DEFAULT_PATH)
find_library(GOBJECT_LIBRARY NAMES gobject-2.0
  PATHS "${GSTREAMER_ROOT}/lib"
  NO_DEFAULT_PATH)
find_library(GSTAPP_LIBRARY NAMES gstapp-1.0
  PATHS "${GSTREAMER_ROOT}/lib"
  NO_DEFAULT_PATH)
find_library(GSTVIDEO_LIBRARY NAMES gstvideo-1.0
  PATHS "${GSTREAMER_ROOT}/lib"
  NO_DEFAULT_PATH)

if(NOT GSTREAMER_INCLUDE_DIR OR NOT GLIB_INCLUDE_DIR OR
   NOT GLIB_CONFIG_INCLUDE_DIR OR NOT GSTAPP_INCLUDE_DIR OR
   NOT GSTREAMER_LIBRARY OR NOT GLIB_LIBRARY OR NOT GOBJECT_LIBRARY OR
   NOT GSTAPP_LIBRARY OR NOT GSTVIDEO_LIBRARY)
  message(FATAL_ERROR
    "GStreamer x64 development files are required. Set GSTREAMER_ROOT to the MSVC development package root.")
endif()

set(GST_INCLUDE_DIRS
  "${GSTREAMER_INCLUDE_DIR}"
  "${GSTAPP_INCLUDE_DIR}"
  "${GLIB_INCLUDE_DIR}"
  "${GLIB_CONFIG_INCLUDE_DIR}")
set(GST_LIBRARIES "${GSTREAMER_LIBRARY}" "${GSTAPP_LIBRARY}" "${GSTVIDEO_LIBRARY}" "${GLIB_LIBRARY}" "${GOBJECT_LIBRARY}")

add_library(airplay_bridge_core STATIC
  last_frame.c
  pipeline.c
  direct_pipeline.c
  scheduled_video_buffer.c
  source_clock_events.c
  source_clock_recording_finalize.c
  source_clock_stop_request.c
  source_clock_rtsp_termination.c
  source_clock_pipeline.c
  source_clock_encoder_policy.c
  source_clock_protocol.c
  source_clock_video_watermark.c
  source_clock_video_proof.c
  source_clock_timeline.c
  source_clock_reader.c
  source_clock_audio_bin.c
  source_clock_fixed_pcm_audio.c
  source_clock_audio_frame_clock.c
  source_clock_audio_caps_state.c
  video_input.c
  video_au_gate.c
  video_scheduler.c
  source_clock_video_pending_fifo.c)
target_include_directories(airplay_bridge_core PRIVATE ${GST_INCLUDE_DIRS})
target_link_libraries(airplay_bridge_core PRIVATE ${GST_LIBRARIES})
option(IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO
  "Start source-clock comparison runtime without real audio ingress" OFF)
if(IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_START_WITHOUT_REAL_AUDIO=1)
endif()
option(IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN
  "Opt in to the staged source-clock AAC-LC dynamic audio bin" OFF)
if(IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN=1)
endif()
option(IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO
  "Opt in to the staged source-clock fixed PCM late-audio boundary" OFF)
if(IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO=1)
endif()
if(IMAGEPAD_SOURCE_CLOCK_FIXED_PCM_AUDIO AND IMAGEPAD_SOURCE_CLOCK_DYNAMIC_AUDIO_BIN)
  message(FATAL_ERROR
    "FIXED_PCM_AUDIO and DYNAMIC_AUDIO_BIN are mutually exclusive staged candidates")
endif()
option(IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY
  "Diagnostic: omit source-clock RTSP audio track" OFF)
if(IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_RTSP_VIDEO_ONLY=1)
endif()
option(IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER
  "Diagnostic: let rtspclientsink choose the source-clock AAC payloader" OFF)
if(IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_AUTO_AUDIO_PAYLOADER=1)
endif()
option(IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX
  "Diagnostic: disable rtspclientsink retransmission management" OFF)
if(IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_DISABLE_RTSP_RTX=1)
endif()
option(IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP
  "Diagnostic: publish source-clock RTP over UDP instead of interleaved RTSP/TCP" OFF)
if(IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP)
  target_compile_definitions(airplay_bridge_core PRIVATE
    IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP=1)
endif()

if(WIN32)
  target_link_libraries(airplay_bridge_core PRIVATE ws2_32)
endif()

add_executable(airplay-gstreamer-bridge main.c)
target_include_directories(airplay-gstreamer-bridge PRIVATE ${GST_INCLUDE_DIRS})
target_link_libraries(airplay-gstreamer-bridge PRIVATE airplay_bridge_core ${GST_LIBRARIES})

add_executable(airplay-gstreamer-capability-probe capability_probe.c)
target_include_directories(airplay-gstreamer-capability-probe PRIVATE ${GST_INCLUDE_DIRS})
target_link_libraries(airplay-gstreamer-capability-probe PRIVATE ${GST_LIBRARIES})

include(CTest)
if(BUILD_TESTING)
  add_executable(test_candidate_cli tests/test_candidate_cli.c)
  target_include_directories(test_candidate_cli PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  target_link_libraries(test_candidate_cli PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_candidate_cli COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    $<TARGET_FILE:test_candidate_cli>)
  add_executable(test_source_clock_video_proof
    tests/test_source_clock_video_proof.c
    source_clock_video_proof.c)
  target_include_directories(test_source_clock_video_proof PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR} ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  add_test(NAME airplay_source_clock_video_proof
    COMMAND $<TARGET_FILE:test_source_clock_video_proof>)
  set_tests_properties(airplay_source_clock_video_proof PROPERTIES TIMEOUT 10)

  add_executable(test_source_clock_video_watermark
    tests/test_source_clock_video_watermark.c
    source_clock_video_watermark.c)
  target_include_directories(test_source_clock_video_watermark PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR} ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  add_test(NAME airplay_source_clock_video_watermark
    COMMAND $<TARGET_FILE:test_source_clock_video_watermark>)
  set_tests_properties(airplay_source_clock_video_watermark PROPERTIES TIMEOUT 10)

  add_executable(test_last_frame tests/test_last_frame.c)
  target_include_directories(test_last_frame PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_last_frame PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_last_frame COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_last_frame>)

  add_executable(test_source_clock_protocol tests/test_source_clock_protocol.c)
  target_include_directories(test_source_clock_protocol PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_protocol PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_protocol COMMAND $<TARGET_FILE:test_source_clock_protocol>)

  add_executable(test_source_clock_timeline tests/test_source_clock_timeline.c)
  target_include_directories(test_source_clock_timeline PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_timeline PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_timeline COMMAND $<TARGET_FILE:test_source_clock_timeline>)

  add_executable(test_check_probe tests/test_check_probe.c)
  target_include_directories(test_check_probe PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  add_test(NAME airplay_test_check_probe COMMAND ${CMAKE_COMMAND}
    "-DPROBE=$<TARGET_FILE:test_check_probe>"
    -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_check_probe.cmake")

  add_test(NAME airplay_capability_probe_contract COMMAND ${CMAKE_COMMAND}
    "-DPROBE=$<TARGET_FILE:airplay-gstreamer-capability-probe>"
    "-DGSTREAMER_RUNTIME_ROOT=${GSTREAMER_RUNTIME_ROOT}"
    -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_capability_probe.cmake")
  set_tests_properties(airplay_capability_probe_contract PROPERTIES TIMEOUT 15)

  find_program(GO_EXECUTABLE NAMES go REQUIRED)

  add_executable(test_source_clock_events
    tests/test_source_clock_events.c
    source_clock_events.c)
  target_include_directories(test_source_clock_events PRIVATE
    ${CMAKE_CURRENT_SOURCE_DIR}
    ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  add_test(NAME airplay_source_clock_events
    COMMAND $<TARGET_FILE:test_source_clock_events>)

  add_test(NAME airplay_source_clock_events_boundary
    COMMAND ${CMAKE_COMMAND} -E env
      "IMAGEPAD_SOURCE_CLOCK_EVENTS_TEST_EXE=$<TARGET_FILE:test_source_clock_events>"
      "${GO_EXECUTABLE}" test ./internal/airplaycontract
      -run "^TestNativeSourceClockEventWriterBoundary$"
      -count=1)
  set_tests_properties(airplay_source_clock_events_boundary PROPERTIES
    WORKING_DIRECTORY "${CMAKE_CURRENT_SOURCE_DIR}/../..")

  add_executable(test_source_clock_recording_finalize
    tests/test_source_clock_recording_finalize.c)
  target_include_directories(test_source_clock_recording_finalize PRIVATE
    ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR}
    ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  target_link_libraries(test_source_clock_recording_finalize PRIVATE
    airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_recording_finalize
    COMMAND ${CMAKE_COMMAND} -E env
      "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
      "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
      "GST_PLUGIN_SYSTEM_PATH_1_0="
      $<TARGET_FILE:test_source_clock_recording_finalize>)

  add_executable(test_source_clock_stop_request
    tests/test_source_clock_stop_request.c)
  target_include_directories(test_source_clock_stop_request PRIVATE
    ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_stop_request PRIVATE
    airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_stop_request
    COMMAND ${CMAKE_COMMAND} -E env
      "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
      "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
      "GST_PLUGIN_SYSTEM_PATH_1_0="
      $<TARGET_FILE:test_source_clock_stop_request>)

  add_executable(test_source_clock_reader tests/test_source_clock_reader.c)
  target_include_directories(test_source_clock_reader PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_reader PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_reader COMMAND $<TARGET_FILE:test_source_clock_reader>)

  add_executable(test_source_clock_audio_frame_clock tests/test_source_clock_audio_frame_clock.c)
  target_include_directories(test_source_clock_audio_frame_clock PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_audio_frame_clock PRIVATE airplay_bridge_core)
  add_test(NAME airplay_source_clock_audio_frame_clock
    COMMAND $<TARGET_FILE:test_source_clock_audio_frame_clock>)

  add_executable(test_source_clock_video_pending_fifo
    tests/test_source_clock_video_pending_fifo.c)
  target_include_directories(test_source_clock_video_pending_fifo PRIVATE
    ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_video_pending_fifo PRIVATE
    airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_video_pending_fifo
    COMMAND $<TARGET_FILE:test_source_clock_video_pending_fifo>)

  add_executable(test_source_clock_audio_caps_state tests/test_source_clock_audio_caps_state.c)
  target_include_directories(test_source_clock_audio_caps_state PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_audio_caps_state PRIVATE airplay_bridge_core)
  add_test(NAME airplay_source_clock_audio_caps_state
    COMMAND $<TARGET_FILE:test_source_clock_audio_caps_state>)

  add_executable(test_scheduled_video_buffer tests/test_scheduled_video_buffer.c)
  target_include_directories(test_scheduled_video_buffer PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_scheduled_video_buffer PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_scheduled_video_buffer COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_scheduled_video_buffer>)

  add_executable(test_source_clock_rtsp_policy tests/test_source_clock_rtsp_policy.c)
  target_include_directories(test_source_clock_rtsp_policy PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_rtsp_policy PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  if(IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP)
    target_compile_definitions(test_source_clock_rtsp_policy PRIVATE
      IMAGEPAD_SOURCE_CLOCK_RTSP_PUBLISH_UDP=1)
  endif()
  add_test(NAME airplay_source_clock_rtsp_policy COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_source_clock_rtsp_policy>)

  add_executable(test_source_clock_single_slice tests/test_source_clock_single_slice.c)
  target_include_directories(test_source_clock_single_slice PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_single_slice PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_single_slice COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_source_clock_single_slice>)
  set_tests_properties(airplay_source_clock_single_slice PROPERTIES TIMEOUT 30)

  add_executable(test_source_clock_rtsp_termination
    tests/test_source_clock_rtsp_termination.c)
  target_include_directories(test_source_clock_rtsp_termination PRIVATE
    ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR}
    ${CMAKE_CURRENT_SOURCE_DIR}/tests)
  target_link_libraries(test_source_clock_rtsp_termination PRIVATE
    airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_rtsp_termination
    COMMAND ${CMAKE_COMMAND} -E env
      "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
      "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
      "GST_PLUGIN_SYSTEM_PATH_1_0="
      $<TARGET_FILE:test_source_clock_rtsp_termination>)

  add_executable(test_source_clock_audio_bin tests/test_source_clock_audio_bin.c)
  target_include_directories(test_source_clock_audio_bin PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_audio_bin PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_audio_bin COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_source_clock_audio_bin>)

  add_executable(test_source_clock_pcm_boundary tests/test_source_clock_pcm_boundary.c)
  target_include_directories(test_source_clock_pcm_boundary PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_pcm_boundary PRIVATE ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_pcm_boundary COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_source_clock_pcm_boundary>)

  add_executable(test_source_clock_fixed_pcm_audio tests/test_source_clock_fixed_pcm_audio.c)
  target_include_directories(test_source_clock_fixed_pcm_audio PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_source_clock_fixed_pcm_audio PRIVATE airplay_bridge_core ${GST_LIBRARIES})
  add_test(NAME airplay_source_clock_fixed_pcm_audio COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_source_clock_fixed_pcm_audio>)

  add_executable(test_video_au_gate tests/test_video_au_gate.c)
  target_include_directories(test_video_au_gate PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_video_au_gate PRIVATE airplay_bridge_core)
  add_test(NAME airplay_video_au_gate COMMAND $<TARGET_FILE:test_video_au_gate>)

  add_executable(test_video_scheduler tests/test_video_scheduler.c)
  target_include_directories(test_video_scheduler PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_video_scheduler PRIVATE airplay_bridge_core)
  add_test(NAME airplay_video_scheduler COMMAND $<TARGET_FILE:test_video_scheduler>)

  add_executable(test_video_input tests/test_video_input.c)
  add_test(NAME airplay_candidate_pipeline COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_video_input> --candidate)
  set_tests_properties(airplay_candidate_pipeline PROPERTIES TIMEOUT 20)
  add_test(NAME airplay_candidate_codec COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_video_input> --candidate-codec)
  set_tests_properties(airplay_candidate_codec PROPERTIES TIMEOUT 20)
  foreach(scenario audio interleavings)
    add_test(NAME airplay_candidate_${scenario} COMMAND ${CMAKE_COMMAND} -E env
      "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
      "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
      "GST_PLUGIN_SYSTEM_PATH_1_0="
      $<TARGET_FILE:test_video_input> --candidate-${scenario})
    set_tests_properties(airplay_candidate_${scenario} PROPERTIES TIMEOUT 30)
  endforeach()
  target_include_directories(test_video_input PRIVATE ${GST_INCLUDE_DIRS} ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(test_video_input PRIVATE airplay_bridge_core)
  add_test(NAME airplay_video_input COMMAND ${CMAKE_COMMAND} -E env
    "PATH=${GSTREAMER_RUNTIME_ROOT}/bin"
    "GST_PLUGIN_PATH_1_0=${GSTREAMER_RUNTIME_ROOT}/lib/gstreamer-1.0"
    "GST_PLUGIN_SYSTEM_PATH_1_0="
    $<TARGET_FILE:test_video_input>)

  add_test(NAME airplay_source_clock_fixed_ports_cli
    COMMAND ${CMAKE_COMMAND}
      "-DBRIDGE=$<TARGET_FILE:airplay-gstreamer-bridge>"
      "-DGSTREAMER_RUNTIME_ROOT=${GSTREAMER_RUNTIME_ROOT}"
      "-DTEST_DIR=${CMAKE_CURRENT_BINARY_DIR}/source-clock-fixed-ports-test"
      -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_source_clock_fixed_ports.cmake")

  add_test(NAME airplay_source_clock_timer_cli
    COMMAND ${CMAKE_COMMAND}
      "-DBRIDGE=$<TARGET_FILE:airplay-gstreamer-bridge>"
      "-DGSTREAMER_RUNTIME_ROOT=${GSTREAMER_RUNTIME_ROOT}"
      "-DTEST_DIR=${CMAKE_CURRENT_BINARY_DIR}/source-clock-timer-cli-test"
      -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_source_clock_timer_cli.cmake")

  add_test(NAME airplay_source_clock_schema2_cli
    COMMAND ${CMAKE_COMMAND}
      "-DBRIDGE=$<TARGET_FILE:airplay-gstreamer-bridge>"
      "-DGSTREAMER_RUNTIME_ROOT=${GSTREAMER_RUNTIME_ROOT}"
      "-DTEST_DIR=${CMAKE_CURRENT_BINARY_DIR}/source-clock-schema2-cli-test"
      -P "${CMAKE_CURRENT_SOURCE_DIR}/tests/test_source_clock_schema2_cli.cmake")
endif()
