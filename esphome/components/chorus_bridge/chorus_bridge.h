#pragma once

#ifdef USE_ESP32

#include <atomic>
#include <cstdint>
#include <memory>
#include <string>
#include <vector>

#include "esphome/core/component.h"
#include "esphome/core/helpers.h"

#include "esphome/components/audio/audio_transfer_buffer.h"
#include "esphome/components/microphone/microphone.h"
#include "esphome/components/microphone/microphone_source.h"
#include "esphome/components/ring_buffer/ring_buffer.h"
#include "esphome/components/socket/socket.h"
#include "esphome/components/speaker/speaker.h"

#ifdef USE_CHORUS_BRIDGE_DUCKING
#include "esphome/components/mixer/speaker/mixer_speaker.h"
#endif

namespace esphome::chorus_bridge {

// Stock voice_assistant constants. Matched deliberately: these are tuned to the
// XMOS pipeline's chunk cadence, not arbitrary (SPEC §3.2).
static const uint32_t SAMPLE_RATE_HZ = 16000;
static const size_t RING_BUFFER_SIZE = 512 * SAMPLE_RATE_HZ / 1000 * sizeof(int16_t);
static const size_t SEND_BUFFER_SIZE = 32 * SAMPLE_RATE_HZ / 1000 * sizeof(int16_t);
static const size_t SPEAKER_BUFFER_SIZE = 16 * 1024;

// Read granularity, and the slack above SPEAKER_BUFFER_SIZE that rx_ and
// speaker_pending_ are allowed. Both are reserved once in setup() and never
// grown past it: a repeated 16 KB reallocation out of a fragmented internal
// heap aborts the firmware, because exceptions are off and there is nothing to
// catch std::bad_alloc.
static const size_t RX_CHUNK_SIZE = 4 * 1024;
static const size_t RX_CAPACITY = SPEAKER_BUFFER_SIZE + RX_CHUNK_SIZE;

static const uint8_t PROTOCOL_VERSION = 1;
static const size_t HEADER_SIZE = 4;

// Frame types. See internal/bridge/frame.go, which must stay in step.
enum class FrameType : uint8_t {
  HELLO = 0x01,
  MIC = 0x02,
  WAKE = 0x03,
  PLAYED = 0x04,
  MUTE = 0x05,

  TTS = 0x10,
  STOP = 0x11,
  FINISH = 0x12,
  DUCK = 0x13,
  MIC_ENABLE = 0x14,
};

static const uint8_t MUTE_HARDWARE = 1 << 0;
static const uint8_t MUTE_SOFTWARE = 1 << 1;

// One microphone channel: a non-passive source, its ring buffer, and the
// frame-aligned reader loop() drains.
struct MicChannel {
  microphone::MicrophoneSource *source{nullptr};
  uint8_t channel{0};
  std::unique_ptr<audio::RingBufferAudioSource> reader;
  // weak_ptr so the mic task's one trailing callback after teardown is a
  // no-op rather than a use-after-free. Copied from voice_assistant.cpp:38-52.
  std::weak_ptr<ring_buffer::RingBuffer> ring_buffer;
};

/// Full-duplex audio bridge. Dials the orchestrator over raw TCP because the
/// native API cannot carry audio without forking it (SPEC §3.1).
class ChorusBridge : public Component {
 public:
  void setup() override;
  void loop() override;
  void dump_config() override;
  float get_setup_priority() const override { return setup_priority::AFTER_CONNECTION; }

  void set_host(const std::string &host) { this->host_ = host; }
  void set_port(uint16_t port) { this->port_ = port; }
  void set_reconnect_interval(uint32_t ms) { this->reconnect_interval_ = ms; }
  void set_microphone(microphone::Microphone *mic) { this->mic_ = mic; }
  void set_speaker(speaker::Speaker *spk) { this->speaker_ = spk; }
  void add_microphone_source(microphone::MicrophoneSource *source, uint8_t channel);
#ifdef USE_CHORUS_BRIDGE_DUCKING
  void set_ducking_speaker(mixer_speaker::SourceSpeaker *spk) { this->ducking_speaker_ = spk; }
#endif

  /// Wake-word hook for YAML. micro_wake_word stays on-device and independent
  /// of voice_assistant, so it reaches us through an automation (SPEC §3.2).
  void on_wake_word(const std::string &wake_word);

  bool is_connected() const { return this->socket_ != nullptr && this->handshake_sent_; }

 protected:
  void start_connect_();
  bool finish_connect_();
  void disconnect_(const char *reason);

  void queue_frame_(FrameType type, uint8_t flags, const uint8_t *payload, size_t length);
  void queue_frame_(FrameType type, uint8_t flags) { this->queue_frame_(type, flags, nullptr, 0); }
  bool flush_tx_();

  void pump_uplink_();
  void pump_downlink_();
  void pump_speaker_();
  void publish_played_();
  void publish_mute_();

  void handle_frame_(FrameType type, uint8_t flags, const uint8_t *payload, size_t length);
  void start_microphones_();
  void stop_microphones_();

  std::string host_;
  uint16_t port_{0};
  uint32_t reconnect_interval_{5000};
  uint32_t last_connect_attempt_{0};

  std::unique_ptr<socket::Socket> socket_;
  struct sockaddr_storage connect_addr_{};
  socklen_t connect_addrlen_{0};
  bool connecting_{false};
  bool handshake_sent_{false};

  microphone::Microphone *mic_{nullptr};
  speaker::Speaker *speaker_{nullptr};
  std::vector<MicChannel> mic_channels_;
#ifdef USE_CHORUS_BRIDGE_DUCKING
  mixer_speaker::SourceSpeaker *ducking_speaker_{nullptr};
#endif

  std::vector<uint8_t> tx_;  // whole frames only; a partial frame is unparseable
  std::vector<uint8_t> rx_;
  std::vector<uint8_t> speaker_pending_;  // bytes play() refused; backpressure

  // Written on the speaker task, read in loop(). Accumulated here because
  // add_audio_output_callback reports a per-DMA-buffer delta (SPEC §3.2.1).
  std::atomic<uint64_t> played_frames_{0};
  std::atomic<int64_t> played_timestamp_{0};
  std::atomic<bool> played_dirty_{false};

  std::atomic<bool> playing_{false};
  bool finish_requested_{false};
  bool mic_requested_{true};
  bool mic_started_{false};
  uint8_t last_mute_flags_{0xff};
  uint32_t dropped_chunks_{0};
  size_t next_channel_{0};    // round-robin cursor; see pump_uplink_
  bool tx_urgent_{false};     // a control frame is queued; do not coalesce
  uint32_t last_flush_ms_{0};
};

}  // namespace esphome::chorus_bridge

#endif  // USE_ESP32
