#include "chorus_bridge.h"

#ifdef USE_ESP32

#include <algorithm>
#include <cerrno>
#include <cinttypes>
#include <cstring>

#include "esphome/core/application.h"
#include "esphome/core/log.h"

namespace esphome::chorus_bridge {

static const char *const TAG = "chorus_bridge";

// Sized to the mic ring buffers it drains, one per channel. Uplink audio is
// droppable in principle, but a queue that holds less than the ring does turns
// every brief loop() delay into a hole in the capture the ring was sized to
// absorb -- and a 400 ms hole is indistinguishable from going half duplex.
static const size_t TX_CAPACITY = 2 * RING_BUFFER_SIZE;

// Uplink coalescing. Mic audio is flushed once a full TCP segment has built up,
// or once this long has passed, whichever comes first. A 32 ms chunk is 1028
// bytes, and on a busy 2.4 GHz band a short frame costs nearly the same airtime
// as a full one, so flushing per chunk spends the band on headers: measured on
// the device, batching to an MSS halved the EAGAIN rate for the same bytes.
// The deadline bounds the added capture latency, which only has to stay inside
// the host's endpointing window -- barge-in latency is the downlink's STOP.
static const size_t UPLINK_COALESCE_BYTES = 1400;
static const uint32_t UPLINK_FLUSH_MS = 100;

static void put_be16(std::vector<uint8_t> &out, uint16_t v) {
  out.push_back(v >> 8);
  out.push_back(v & 0xff);
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
  // Derived once, then the PSK is wiped: the link never needs the API's key.
  if (!auth::derive_link_key(this->psk_.data(), this->link_key_.data())) {
    ESP_LOGE(TAG, "Link key derivation failed");
    this->mark_failed();
    return;
  }
  mbedtls_platform_zeroize(this->psk_.data(), this->psk_.size());

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
    if (now - this->last_connect_attempt_ >= this->reconnect_interval_ + this->backoff_ms_) {
      this->last_connect_attempt_ = now;
      this->start_connect_();
    }
    return;
  }
  if (this->connecting_ && !this->finish_connect_()) {
    // lwip retries a SYN for over a minute; a host that slow is not reachable.
    if (this->connecting_ && millis() - this->attempt_started_ >= HANDSHAKE_TIMEOUT_MS) {
      this->disconnect_("connect timed out");
    }
    return;
  }

  this->pump_downlink_();
  if (this->socket_ == nullptr) {
    return;
  }
  if (this->authenticated_) {
    this->publish_mute_();
    this->publish_played_();
    this->pump_uplink_();
    this->pump_speaker_();
  } else if (millis() - this->connected_at_ >= HANDSHAKE_TIMEOUT_MS) {
    // millis(), not now: finish_connect_ above may have stamped a later one.
    this->disconnect_("the host did not prove itself in time");
    return;
  }
  if (this->tx_urgent_ || this->tx_.size() >= UPLINK_COALESCE_BYTES ||
      now - this->last_flush_ms_ >= UPLINK_FLUSH_MS) {
    this->tx_urgent_ = false;
    this->last_flush_ms_ = now;
    this->flush_tx_();
  }
}

void ChorusBridge::start_connect_() {
  // Monitored so the main loop wakes on inbound TTS instead of polling.
  this->socket_ = socket::socket_loop_monitored(AF_INET, SOCK_STREAM, 0);
  if (this->socket_ == nullptr) {
    ESP_LOGW(TAG, "Socket creation failed");
    this->set_link_state_(false);
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
    this->set_link_state_(false);
    return;
  }
  if (this->socket_->connect((struct sockaddr *) &this->connect_addr_, this->connect_addrlen_) != 0 &&
      errno != EINPROGRESS) {
    ESP_LOGW(TAG, "Connect to %s:%u failed: %s", this->host_.c_str(), this->port_, strerror(errno));
    this->socket_ = nullptr;
    this->set_link_state_(false);
    return;
  }
  this->connecting_ = true;
  this->attempt_started_ = millis();
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
  // YAML change can alter the channel count. It names the satellite it is,
  // as devices.yaml does, MAC suffix included, and brings a fresh nonce the
  // host's proof must cover. Nothing else goes out until that proof holds.
  uint8_t nonce[auth::NONCE_SIZE];
  if (!random_bytes(nonce, sizeof(nonce))) {
    this->disconnect_("no random bytes for the nonce");
    return false;
  }
  const StringRef &name = App.get_name();
  this->hello_len_ = auth::encode_hello(PROTOCOL_VERSION, SAMPLE_RATE_HZ, 8 * sizeof(int16_t),
                                        this->mic_channels_.size(), nonce, name.c_str(), name.size(),
                                        this->hello_.data());
  if (this->hello_len_ == 0) {
    this->disconnect_("node name does not fit the hello");
    return false;
  }
  if (!this->queue_plain_(FrameType::HELLO, this->hello_.data(), this->hello_len_)) {
    return false;
  }
  this->connected_at_ = millis();

  ESP_LOGI(TAG, "Connected to %s:%u, awaiting the host's proof", this->host_.c_str(), this->port_);
  return true;
}

void ChorusBridge::verify_host_(const uint8_t *payload, size_t length) {
  if (length != auth::CHALLENGE_SIZE) {
    this->disconnect_("malformed challenge");
    return;
  }
  const uint8_t *host_nonce = payload;
  const uint8_t *proof = payload + auth::NONCE_SIZE;
  uint8_t want[auth::MAC_SIZE];
  if (!auth::host_proof(this->link_key_.data(), this->hello_.data(), this->hello_len_, host_nonce, want)) {
    this->disconnect_("host proof failed to compute");
    return;
  }
  const bool proven = auth::equal(proof, want, sizeof(want));
  mbedtls_platform_zeroize(want, sizeof(want));
  if (!proven) {
    // No microphone for a host without the key. Backed off, so a host that
    // took orchestrator_host's address is not redialled every interval.
    this->backoff_ms_ =
        std::min<uint32_t>(MAX_BACKOFF_MS, std::max<uint32_t>(this->reconnect_interval_, 2 * this->backoff_ms_));
    ESP_LOGE(TAG, "The host at %s did not prove it holds this satellite's key; next dial in %" PRIu32 " ms",
             this->host_.c_str(), this->reconnect_interval_ + this->backoff_ms_);
    this->disconnect_("host proof refused");
    return;
  }

  uint8_t answer[auth::MAC_SIZE];
  uint8_t up[auth::KEY_SIZE];
  uint8_t down[auth::KEY_SIZE];
  const bool keyed =
      auth::device_proof(this->link_key_.data(), this->hello_.data(), this->hello_len_, host_nonce, answer) &&
      auth::derive_session_keys(this->link_key_.data(), this->hello_.data() + auth::HELLO_NONCE_OFFSET, host_nonce,
                                up, down) &&
      this->tx_seal_.set_key(up) && this->rx_seal_.set_key(down);
  mbedtls_platform_zeroize(up, sizeof(up));
  mbedtls_platform_zeroize(down, sizeof(down));
  if (!keyed) {
    this->disconnect_("session keys failed");
    return;
  }
  // The answer leaves in the clear; every frame after it is sealed. A
  // handshake frame: it either queues or tears the link down.
  if (!this->queue_plain_(FrameType::AUTH, answer, sizeof(answer))) {
    return;
  }
  this->authenticated_ = true;
  this->backoff_ms_ = 0;
  this->last_mute_flags_ = 0xff;  // force a mute report on the new connection
  ESP_LOGI(TAG, "The host proved the key; link sealed as %s", App.get_name().c_str());
  if (this->mic_requested_) {
    this->start_microphones_();
  }
  this->set_link_state_(true);
}

void ChorusBridge::set_link_state_(bool connected) {
  const LinkState next = connected ? LinkState::CONNECTED : LinkState::DISCONNECTED;
  if (next == this->link_state_) {
    return;
  }
  this->link_state_ = next;
  if (connected) {
    this->connected_trigger_.trigger();
  } else {
    this->disconnected_trigger_.trigger();
  }
}

void ChorusBridge::disconnect_(const char *reason) {
  ESP_LOGW(TAG, "Disconnected: %s", reason);
  if (this->socket_ != nullptr) {
    this->socket_->close();
    this->socket_ = nullptr;
  }
  this->connecting_ = false;
  this->authenticated_ = false;
  // The session keys go with the connection; the next one derives its own.
  this->tx_seal_.reset();
  this->rx_seal_.reset();
  this->rx_head_open_ = false;
  this->hello_len_ = 0;
  // Before stop(), so no straggling callback is attributed to the next link.
  this->playing_.store(false, std::memory_order_release);
  this->tx_.clear();
  this->rx_.clear();
  this->speaker_pending_.clear();
  this->finish_requested_ = false;
  this->stop_ack_ = 0;
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
  // Last: the automation it runs sees the link already down.
  this->set_link_state_(false);
}

bool ChorusBridge::queue_plain_(FrameType type, const uint8_t *payload, size_t length) {
  if (this->socket_ == nullptr || this->connecting_ || this->authenticated_ ||
      (type != FrameType::HELLO && type != FrameType::AUTH)) {
    return false;
  }
  // Only ever into an empty or near-empty queue: these are a connection's
  // first two frames.
  if (this->tx_.size() + HEADER_SIZE + length > TX_CAPACITY) {
    this->disconnect_("TX overflow on a handshake frame");
    return false;
  }
  this->tx_urgent_ = true;
  this->tx_.push_back(static_cast<uint8_t>(type));
  this->tx_.push_back(0);
  put_be16(this->tx_, length);
  this->tx_.insert(this->tx_.end(), payload, payload + length);
  return true;
}

bool ChorusBridge::queue_frame_(FrameType type, uint8_t flags, const uint8_t *payload, size_t length) {
  if (this->socket_ == nullptr || this->connecting_) {
    return false;
  }
  // Nothing is sealed before the host has proved the key: audio for a host
  // nobody has verified is not ours to send.
  if (!this->authenticated_ || !this->tx_seal_.keyed()) {
    return false;
  }
  // Append whole records only: a truncated one leaves the host unable to
  // find the next record boundary, and fails its tag.
  if (this->tx_.size() + seal::OVERHEAD + length > TX_CAPACITY) {
    // MIC is droppable audio; the host sees a gap. PLAYED is droppable because
    // it carries a cumulative frame count, so the next one supersedes it --
    // tearing the link down over a status report loses the whole utterance.
    if (type == FrameType::MIC || type == FrameType::PLAYED) {
      if (++this->dropped_chunks_ % 32 == 1) {
        ESP_LOGW(TAG, "TX full, %" PRIu32 " uplink frames not queued", this->dropped_chunks_);
      }
      return false;
    }
    this->disconnect_("TX overflow on a control frame");
    return false;
  }
  // Laid out as chorus_seal.h seals it: room for the length, the frame, room
  // for the tag. Within the capacity reserved in setup(), so nothing moves.
  const size_t start = this->tx_.size();
  this->tx_.resize(start + seal::RECORD_HEADER_SIZE);
  this->tx_.push_back(static_cast<uint8_t>(type));
  this->tx_.push_back(flags);
  if (length > 0) {
    this->tx_.insert(this->tx_.end(), payload, payload + length);
  }
  this->tx_.resize(this->tx_.size() + seal::TAG_SIZE);
  if (!this->tx_seal_.seal(this->tx_.data() + start, seal::INNER_HEADER_SIZE + length)) {
    this->disconnect_("sealing a frame failed");
    return false;
  }
  // Not audio means control: flush it now rather than behind the deadline.
  if (type != FrameType::MIC) {
    this->tx_urgent_ = true;
  }
  return true;
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
  // Round robin, resuming after the channel served last. Draining channel 0 to
  // empty before touching channel 1 starves channel 1 outright once the socket
  // ever blocks: measured on the device, channel 1's ring sat 100% full for a
  // whole session while channel 0 stayed under 1 KB, so the raw channel never
  // reached the host at all (SPEC §3.2 wants both).
  const size_t count = this->mic_channels_.size();
  for (size_t i = 0; i < count; i++) {
    MicChannel &ch = this->mic_channels_[(this->next_channel_ + i) % count];
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
      // Consumed only once queued. Consuming a dropped frame throws away the
      // ring's congestion buffer the moment the queue is merely near full: 31
      // chunks leave 900 bytes, which fits no frame, so this loop would consume
      // and discard the whole ring rather than hold it until the socket drains.
      // Leaving it exposed retries the same bytes next loop(), which is also why
      // this breaks -- fill() returns 0 while an exposure is unconsumed, so
      // continuing would spin.
      if (!this->queue_frame_(FrameType::MIC, ch.channel, ch.reader->data(), available)) {
        break;  // give the next channel its turn; this one keeps its data
      }
      ch.reader->consume(available);
      // Not flushed per frame: see UPLINK_COALESCE_BYTES. loop() decides.
    }
  }
  if (count > 0) {
    this->next_channel_ = (this->next_channel_ + 1) % count;
  }
}

void ChorusBridge::pump_downlink_() {
  // Bounded, not drained. An utterance is megabytes and this heap is not: an
  // unbounded rx_ aborts the firmware in std::vector::insert the moment the
  // allocator fails, because exceptions are off. Leaving the socket unread
  // instead makes TCP flow control hold the remainder on the host.
  //
  // Read up to the capacity, not up to the last whole chunk: a frame that
  // fits rx_ must be able to complete, or the parser below waits forever.
  while (this->rx_.size() < RX_CAPACITY) {
    const size_t offset = this->rx_.size();
    const size_t want = std::min(RX_CHUNK_SIZE, RX_CAPACITY - offset);
    this->rx_.resize(offset + want);
    ssize_t got = this->socket_->read(this->rx_.data() + offset, want);
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
  while (!this->authenticated_ && this->rx_.size() - consumed >= HEADER_SIZE) {
    // Before the host has proved the key the only frame it may send is its
    // challenge, in the clear, so nothing longer is waited for.
    const uint8_t *h = this->rx_.data() + consumed;
    const size_t length = (static_cast<size_t>(h[2]) << 8) | h[3];
    if (static_cast<FrameType>(h[0]) != FrameType::CHALLENGE) {
      this->disconnect_("host frame before the challenge");
      return;
    }
    if (length != auth::CHALLENGE_SIZE) {
      this->disconnect_("malformed challenge");
      return;
    }
    if (this->rx_.size() - consumed - HEADER_SIZE < length) {
      break;  // partial frame; wait for the rest
    }
    this->verify_host_(h + HEADER_SIZE, length);
    consumed += HEADER_SIZE + length;
    if (this->socket_ == nullptr) {
      return;
    }
  }
  while (this->authenticated_ && this->rx_.size() - consumed >= seal::RECORD_HEADER_SIZE) {
    uint8_t *record = this->rx_.data() + consumed;
    const size_t sealed_len = (static_cast<size_t>(record[0]) << 8) | record[1];
    // The wire allows 65535 bytes; rx_ holds RX_CAPACITY and never grows. A
    // longer record can never complete, so waiting for it wedges the link with
    // no disconnect. The host sends 1 KB TTS chunks (internal/bridge/link.go),
    // so this is a peer that is not the host, not backpressure.
    if (sealed_len > RX_CAPACITY - seal::RECORD_HEADER_SIZE) {
      this->disconnect_("frame too large");
      return;
    }
    const size_t record_len = seal::RECORD_HEADER_SIZE + sealed_len;
    if (this->rx_.size() - consumed < record_len) {
      break;  // partial record; wait for the rest
    }
    // Opened once, in place. Altered, replayed, reordered, or sealed under
    // another key: nothing in it is acted on, and the link drops.
    if (!this->rx_head_open_ && !this->rx_seal_.open(record, record_len)) {
      this->disconnect_("a frame did not authenticate");
      return;
    }
    this->rx_head_open_ = true;
    const uint8_t *inner = record + seal::RECORD_HEADER_SIZE;
    const FrameType type = static_cast<FrameType>(inner[0]);
    // Same bound one level down: stop handing audio to the speaker queue while
    // it is already full, and leave the opened record in rx_ for a later loop().
    if (type == FrameType::TTS && this->speaker_pending_.size() >= SPEAKER_BUFFER_SIZE) {
      break;
    }
    this->rx_head_open_ = false;
    this->handle_frame_(type, inner[1], inner + seal::INNER_HEADER_SIZE,
                        sealed_len - seal::TAG_SIZE - seal::INNER_HEADER_SIZE);
    consumed += record_len;
    if (this->socket_ == nullptr) {
      return;
    }
  }
  if (consumed > 0) {
    this->rx_.erase(this->rx_.begin(), this->rx_.begin() + consumed);
  }
}

// A sealed frame from the host. The challenge is handled before any of these
// (pump_downlink_), and only once per connection.
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
      // Answered with a PLAYED carrying this tag even if the position did not
      // move: a routine report can be in flight when the host sends the stop,
      // so the host waits for the one report that follows it (ADR-0033).
      this->stop_ack_ = flags;
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

    case FrameType::CHALLENGE:
      this->disconnect_("a second challenge");
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
  const uint8_t ack = this->stop_ack_;
  if (!this->played_dirty_.exchange(false, std::memory_order_acquire) && ack == 0) {
    return;
  }
  std::vector<uint8_t> p;
  put_be64(p, this->played_frames_.load(std::memory_order_relaxed));
  put_be64(p, static_cast<uint64_t>(this->played_timestamp_.load(std::memory_order_relaxed)));
  // A stop's answer is retried, not dropped: the host waits for this one
  // report, and no later routine report can stand in for it.
  if (this->queue_frame_(FrameType::PLAYED, ack, p.data(), p.size())) {
    this->stop_ack_ = 0;
  }
}

void ChorusBridge::publish_mute_() {
  // Reported, never negotiated: hardware mute is authoritative and there is no
  // host frame that could clear it.
  uint8_t flags = 0;
#ifdef USE_CHORUS_BRIDGE_HARDWARE_MUTE
  // A switch not read yet counts as on: not knowing is not consent.
  if (this->hardware_mute_ != nullptr && (!this->hardware_mute_->has_state() || this->hardware_mute_->state)) {
    flags |= MUTE_HARDWARE;
  }
#endif
  // get_mute_state() is microphone.mute's flag, a software zero-fill, so it
  // is never the hardware bit.
  if (this->mic_ != nullptr && this->mic_->get_mute_state()) {
    flags |= MUTE_SOFTWARE;
  }
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
#ifdef USE_CHORUS_BRIDGE_HARDWARE_MUTE
  ESP_LOGCONFIG(TAG, "  Hardware mute switch: %s", YESNO(this->hardware_mute_ != nullptr));
#else
  ESP_LOGCONFIG(TAG, "  Hardware mute switch: NO");
#endif
  ESP_LOGCONFIG(TAG, "  Protocol: %u, sealed AES-256-GCM, authenticates as %s", PROTOCOL_VERSION,
                App.get_name().c_str());
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
