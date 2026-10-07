#include "chorus_bridge.h"

#ifdef USE_ESP32

#include <cerrno>
#include <cinttypes>
#include <cstring>

#include "esphome/core/log.h"

namespace esphome::chorus_bridge {

static const char *const TAG = "chorus_bridge";

// Sized to the mic ring buffers it drains, one per channel. Uplink audio is
// droppable in principle, but a queue that holds less than the ring does turns
// every brief loop() delay into a hole in the capture the ring was sized to
// absorb -- and a 400 ms hole is indistinguishable from going half duplex.
static const size_t TX_CAPACITY = 2 * RING_BUFFER_SIZE;

static void put_be16(std::vector<uint8_t> &out, uint16_t v) {
  out.push_back(v >> 8);
  out.push_back(v & 0xff);
}

static void put_be32(std::vector<uint8_t> &out, uint32_t v) {
  for (int shift = 24; shift >= 0; shift -= 8)
    out.push_back((v >> shift) & 0xff);
}

static void put_be64(std::vector<uint8_t> &out, uint64_t v) {
  for (int shift = 56; shift >= 0; shift -= 8)
    out.push_back((v >> shift) & 0xff);
}

void ChorusBridge::add_microphone_source(microphone::MicrophoneSource *source, uint8_t channel) {
  MicChannel ch;
  ch.source = source;
  ch.channel = channel;
  this->mic_channels_.push_back(std::move(ch));
}

void ChorusBridge::setup() {
  this->tx_.reserve(TX_CAPACITY);
  this->rx_.reserve(RX_CAPACITY);
  this->speaker_pending_.reserve(RX_CAPACITY);

  for (auto &ch : this->mic_channels_) {
    // shared, not the returned unique_ptr: the audio source owns the buffer
    // and the mic callback holds only a weak_ptr to it.
    std::shared_ptr<ring_buffer::RingBuffer> rb = ring_buffer::RingBuffer::create(RING_BUFFER_SIZE);
    if (rb == nullptr) {
      ESP_LOGE(TAG, "Ring buffer allocation failed");
      this->mark_failed();
      return;
    }
    // Frame alignment: a half sample on the wire desynchronises the host.
    ch.reader = audio::RingBufferAudioSource::create(rb, SEND_BUFFER_SIZE, sizeof(int16_t));
    if (ch.reader == nullptr) {
      ESP_LOGE(TAG, "Audio source creation failed");
      this->mark_failed();
      return;
    }
    ch.ring_buffer = rb;

    // Fires on the mic's FreeRTOS task, not loop(). Blocking is legal here but
    // sockets, the API and App are not (SPEC §3.2), so this only writes.
    std::weak_ptr<ring_buffer::RingBuffer> weak = ch.ring_buffer;
    ch.source->add_data_callback([weak](const std::vector<uint8_t> &data) {
      std::shared_ptr<ring_buffer::RingBuffer> rb = weak.lock();
      if (rb != nullptr) {
        rb->write((void *) data.data(), data.size());
      }
    });
  }

  if (this->speaker_ != nullptr) {
    // The resampler/mixer/i2s stack converts up from here.
    this->speaker_->set_audio_stream_info(
        audio::AudioStreamInfo(sizeof(int16_t) * 8, 1, SAMPLE_RATE_HZ));

    // Runs on the speaker's own task: copy and hand off, do no work (SPEC §3.2.1).
    this->speaker_->add_audio_output_callback([this](uint32_t frames, int64_t timestamp) {
      // Gated, because stop() is asynchronous: the mixer and the i2s DMA keep
      // draining for up to buffer_duration after it returns, and those frames
      // belong to the stream that was torn down. Counting them charges the next
      // utterance for the tail of the previous one.
      if (!this->playing_.load(std::memory_order_acquire)) {
        return;
      }
      this->played_frames_.fetch_add(frames, std::memory_order_relaxed);
      this->played_timestamp_.store(timestamp, std::memory_order_relaxed);
      this->played_dirty_.store(true, std::memory_order_release);
    });
  }
}

void ChorusBridge::loop() {
  const uint32_t now = millis();

  if (this->socket_ == nullptr) {
    if (now - this->last_connect_attempt_ >= this->reconnect_interval_) {
      this->last_connect_attempt_ = now;
      this->start_connect_();
    }
    return;
  }
  if (this->connecting_ && !this->finish_connect_()) {
    return;
  }

  this->pump_downlink_();
  if (this->socket_ == nullptr) {
    return;
  }
  this->publish_mute_();
  this->publish_played_();
  this->pump_uplink_();
  this->pump_speaker_();
  this->flush_tx_();
}

void ChorusBridge::start_connect_() {
  // Monitored so the main loop wakes on inbound TTS instead of polling.
  this->socket_ = socket::socket_loop_monitored(AF_INET, SOCK_STREAM, 0);
  if (this->socket_ == nullptr) {
    ESP_LOGW(TAG, "Socket creation failed");
    return;
  }
  this->socket_->setblocking(false);
  int nodelay = 1;
  // Nagle would coalesce the 32 ms chunks barge-in timing depends on.
  this->socket_->setsockopt(IPPROTO_TCP, TCP_NODELAY, &nodelay, sizeof(nodelay));

  this->connect_addr_ = {};
  // Kept, because finish_connect_() completes the handshake by re-issuing it.
  this->connect_addrlen_ =
      socket::set_sockaddr((struct sockaddr *) &this->connect_addr_, sizeof(this->connect_addr_), this->host_, this->port_);
  if (this->connect_addrlen_ == 0) {
    ESP_LOGW(TAG, "Bad host '%s'", this->host_.c_str());
    this->socket_ = nullptr;
    return;
  }
  if (this->socket_->connect((struct sockaddr *) &this->connect_addr_, this->connect_addrlen_) != 0 &&
      errno != EINPROGRESS) {
    ESP_LOGW(TAG, "Connect to %s:%u failed: %s", this->host_.c_str(), this->port_, strerror(errno));
    this->socket_ = nullptr;
    return;
  }
  this->connecting_ = true;
}

bool ChorusBridge::finish_connect_() {
  // A second connect(), not SO_ERROR. lwip leaves SO_ERROR at 0 while a
  // connect is still in flight, so SO_ERROR cannot tell "connected" from
  // "pending" -- it reports success early and the first read then fails with
  // EINPROGRESS and tears the link down. Re-issuing connect() is unambiguous:
  // EISCONN means established, EALREADY means keep waiting.
  if (this->socket_->connect((struct sockaddr *) &this->connect_addr_, this->connect_addrlen_) != 0) {
    if (errno == EALREADY || errno == EINPROGRESS) {
      return false;
    }
    if (errno != EISCONN) {
      this->disconnect_(strerror(errno));
      return false;
    }
  }
  this->connecting_ = false;

  // The device states its format rather than letting the host assume it: a
  // YAML change can alter the channel count.
  std::vector<uint8_t> hello;
  hello.push_back(PROTOCOL_VERSION);
  put_be32(hello, SAMPLE_RATE_HZ);
  hello.push_back(8 * sizeof(int16_t));
  hello.push_back(this->mic_channels_.size());
  this->queue_frame_(FrameType::HELLO, 0, hello.data(), hello.size());
  this->handshake_sent_ = true;
  this->last_mute_flags_ = 0xff;  // force a mute report on the new connection

  ESP_LOGI(TAG, "Connected to %s:%u", this->host_.c_str(), this->port_);
  if (this->mic_requested_) {
    this->start_microphones_();
  }
  return true;
}

void ChorusBridge::disconnect_(const char *reason) {
  ESP_LOGW(TAG, "Disconnected: %s", reason);
  if (this->socket_ != nullptr) {
    this->socket_->close();
    this->socket_ = nullptr;
  }
  this->connecting_ = false;
  this->handshake_sent_ = false;
  // Before stop(), so no straggling callback is attributed to the next link.
  this->playing_.store(false, std::memory_order_release);
  this->tx_.clear();
  this->rx_.clear();
  this->speaker_pending_.clear();
  this->finish_requested_ = false;
  // Frames are cumulative per connection (internal/bridge/frame.go), so the
  // counter has to start from zero on the next one. Without this the first
  // PLAYED of a new link reports a position from the previous one.
  this->played_frames_.store(0, std::memory_order_relaxed);
  this->played_dirty_.store(false, std::memory_order_relaxed);
  this->stop_microphones_();
  // Silence rather than play stale TTS when the link returns.
  if (this->speaker_ != nullptr) {
    this->speaker_->stop();
  }
}

void ChorusBridge::queue_frame_(FrameType type, uint8_t flags, const uint8_t *payload, size_t length) {
  if (this->socket_ == nullptr || this->connecting_) {
    return;
  }
  // Append whole frames only: a truncated header leaves the host unable to
  // find the next frame boundary.
  if (this->tx_.size() + HEADER_SIZE + length > TX_CAPACITY) {
    // MIC is droppable audio; the host sees a gap. PLAYED is droppable because
    // it carries a cumulative frame count, so the next one supersedes it --
    // tearing the link down over a status report loses the whole utterance.
    if (type == FrameType::MIC || type == FrameType::PLAYED) {
      if (++this->dropped_chunks_ % 32 == 1) {
        ESP_LOGW(TAG, "TX full, dropped %" PRIu32 " uplink frames", this->dropped_chunks_);
      }
      return;
    }
    this->disconnect_("TX overflow on a control frame");
    return;
  }
  this->tx_.push_back(static_cast<uint8_t>(type));
  this->tx_.push_back(flags);
  put_be16(this->tx_, length);
  if (length > 0) {
    this->tx_.insert(this->tx_.end(), payload, payload + length);
  }
}

bool ChorusBridge::flush_tx_() {
  while (!this->tx_.empty()) {
    ssize_t sent = this->socket_->write(this->tx_.data(), this->tx_.size());
    if (sent > 0) {
      this->tx_.erase(this->tx_.begin(), this->tx_.begin() + sent);
      continue;
    }
    if (sent < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      return false;  // retry next loop; the remainder stays queued
    }
    this->disconnect_(sent == 0 ? "peer closed" : strerror(errno));
    return false;
  }
  return true;
}

void ChorusBridge::pump_uplink_() {
  for (auto &ch : this->mic_channels_) {
    if (ch.reader == nullptr) {
      continue;
    }
    // Delivered chunk size is the mic task's choice; send chunks are sized here.
    // fill(0, false): never block in loop(), and pre_shift is ignored by
    // RingBufferAudioSource, which exposes the ring buffer's storage in place.
    // It returns 0 while an exposure is unconsumed, hence the available() arm.
    while (ch.reader->fill(0, false) > 0 || ch.reader->available() > 0) {
      size_t available = ch.reader->available();
      if (available == 0) {
        break;
      }
      this->queue_frame_(FrameType::MIC, ch.channel, ch.reader->data(), available);
      ch.reader->consume(available);
      if (!this->flush_tx_()) {
        return;  // socket is full or gone; stop draining the ring buffer
      }
    }
  }
}

void ChorusBridge::pump_downlink_() {
  // Bounded, not drained. An utterance is megabytes and this heap is not: an
  // unbounded rx_ aborts the firmware in std::vector::insert the moment the
  // allocator fails, because exceptions are off. Leaving the socket unread
  // instead makes TCP flow control hold the remainder on the host.
  while (this->rx_.size() + RX_CHUNK_SIZE <= RX_CAPACITY) {
    const size_t offset = this->rx_.size();
    this->rx_.resize(offset + RX_CHUNK_SIZE);
    ssize_t got = this->socket_->read(this->rx_.data() + offset, RX_CHUNK_SIZE);
    this->rx_.resize(offset + (got > 0 ? got : 0));
    if (got > 0) {
      continue;
    }
    if (got == 0) {
      this->disconnect_("peer closed");
      return;
    }
    if (errno != EAGAIN && errno != EWOULDBLOCK) {
      this->disconnect_(strerror(errno));
      return;
    }
    break;
  }

  size_t consumed = 0;
  while (this->rx_.size() - consumed >= HEADER_SIZE) {
    const uint8_t *h = this->rx_.data() + consumed;
    const size_t length = (static_cast<size_t>(h[2]) << 8) | h[3];
    if (this->rx_.size() - consumed - HEADER_SIZE < length) {
      break;  // partial frame; wait for the rest
    }
    // Same bound one level down: stop handing audio to the speaker queue while
    // it is already full, and leave the frame in rx_ for a later loop().
    if (static_cast<FrameType>(h[0]) == FrameType::TTS && this->speaker_pending_.size() >= SPEAKER_BUFFER_SIZE) {
      break;
    }
    this->handle_frame_(static_cast<FrameType>(h[0]), h[1], h + HEADER_SIZE, length);
    consumed += HEADER_SIZE + length;
    if (this->socket_ == nullptr) {
      return;
    }
  }
  if (consumed > 0) {
    this->rx_.erase(this->rx_.begin(), this->rx_.begin() + consumed);
  }
}

void ChorusBridge::handle_frame_(FrameType type, uint8_t flags, const uint8_t *payload, size_t length) {
  switch (type) {
    case FrameType::TTS:
      if (this->speaker_ == nullptr) {
        return;
      }
      // Only on a genuine start. TTS arrives as many small frames, and
      // re-entering start() on each one restarts the resampler/mixer chain
      // underneath and audibly stalls it.
      if (this->speaker_->is_stopped()) {
        this->speaker_->start();
      }
      // Not a counter reset: frames stay cumulative for the whole connection
      // (internal/bridge/frame.go), so the host's positions never go backwards.
      this->playing_.store(true, std::memory_order_release);
      this->speaker_pending_.insert(this->speaker_pending_.end(), payload, payload + length);
      this->pump_speaker_();
      return;

    case FrameType::STOP:
      // Barge-in: discard the buffer so the user stops hearing us now. Gate
      // first, so the frames the DAC drains after this are not reported as
      // played -- the last position before a stop is the truncation point.
      this->playing_.store(false, std::memory_order_release);
      this->speaker_pending_.clear();
      this->finish_requested_ = false;
      if (this->speaker_ != nullptr) {
        this->speaker_->stop();
      }
      return;

    case FrameType::FINISH:
      // Deferred, not immediate: the downlink is bounded, so the tail of the
      // utterance is normally still queued here. pump_speaker_() finishes once
      // it has handed the last byte over.
      this->finish_requested_ = true;
      this->pump_speaker_();
      return;

    case FrameType::DUCK:
#ifdef USE_CHORUS_BRIDGE_DUCKING
      if (this->ducking_speaker_ != nullptr && length >= 5) {
        uint32_t duration = 0;
        for (size_t i = 1; i < 5; i++)
          duration = (duration << 8) | payload[i];
        this->ducking_speaker_->apply_ducking(payload[0], duration);
      }
#endif
      return;

    case FrameType::MIC_ENABLE:
      this->mic_requested_ = (flags & 1) != 0;
      if (this->mic_requested_) {
        this->start_microphones_();
      } else {
        this->stop_microphones_();
      }
      return;

    default:
      // Length-delimited, so an unrecognised type is skipped, not fatal.
      ESP_LOGD(TAG, "Ignoring frame type %#02x", static_cast<uint8_t>(type));
      return;
  }
}

void ChorusBridge::pump_speaker_() {
  if (this->speaker_ == nullptr) {
    return;
  }
  if (!this->speaker_pending_.empty()) {
    // play() returns bytes actually buffered: that is the backpressure signal.
    size_t written = this->speaker_->play(this->speaker_pending_.data(), this->speaker_pending_.size());
    if (written > 0) {
      this->speaker_pending_.erase(this->speaker_pending_.begin(), this->speaker_pending_.begin() + written);
    }
  }
  if (this->finish_requested_ && this->speaker_pending_.empty()) {
    this->finish_requested_ = false;
    this->speaker_->finish();
  }
}

void ChorusBridge::publish_played_() {
  if (!this->played_dirty_.exchange(false, std::memory_order_acquire)) {
    return;
  }
  std::vector<uint8_t> p;
  put_be64(p, this->played_frames_.load(std::memory_order_relaxed));
  put_be64(p, static_cast<uint64_t>(this->played_timestamp_.load(std::memory_order_relaxed)));
  this->queue_frame_(FrameType::PLAYED, 0, p.data(), p.size());
}

void ChorusBridge::publish_mute_() {
  if (this->mic_ == nullptr) {
    return;
  }
  // Reported, never negotiated: hardware mute is authoritative and there is no
  // host frame that could clear it.
  uint8_t flags = this->mic_->get_mute_state() ? MUTE_HARDWARE : 0;
  if (flags == this->last_mute_flags_) {
    return;
  }
  this->last_mute_flags_ = flags;
  this->queue_frame_(FrameType::MUTE, flags);
}

void ChorusBridge::on_wake_word(const std::string &wake_word) {
  this->queue_frame_(FrameType::WAKE, 0, reinterpret_cast<const uint8_t *>(wake_word.data()), wake_word.size());
}

void ChorusBridge::start_microphones_() {
  if (this->mic_started_) {
    return;
  }
  for (auto &ch : this->mic_channels_) {
    ch.source->start();
  }
  this->mic_started_ = true;
}

void ChorusBridge::stop_microphones_() {
  if (!this->mic_started_) {
    return;
  }
  for (auto &ch : this->mic_channels_) {
    ch.source->stop();
  }
  this->mic_started_ = false;
}

void ChorusBridge::dump_config() {
  ESP_LOGCONFIG(TAG, "Chorus Bridge:");
  ESP_LOGCONFIG(TAG, "  Orchestrator: %s:%u", this->host_.c_str(), this->port_);
  ESP_LOGCONFIG(TAG, "  Mic channels: %u", (unsigned) this->mic_channels_.size());
  ESP_LOGCONFIG(TAG, "  Speaker: %s", YESNO(this->speaker_ != nullptr));
  for (auto &ch : this->mic_channels_) {
    // A passive source receives audio only while something else started the
    // mic, which ships a silently dead bridge (SPEC §3.2).
    if (ch.source->is_passive()) {
      ESP_LOGE(TAG, "  Channel %u is passive and will never start the mic", ch.channel);
    }
  }
}

}  // namespace esphome::chorus_bridge

#endif  // USE_ESP32
