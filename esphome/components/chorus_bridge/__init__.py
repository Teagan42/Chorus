"""Full-duplex audio bridge between an ESPHome satellite and the Chorus
orchestrator. See docs/SPEC.md §3 and docs/adr/0010."""

import esphome.codegen as cg
from esphome.components import microphone, speaker
from esphome.components.mixer.speaker import SourceSpeaker
import esphome.config_validation as cv
from esphome.const import (
    CONF_CHANNELS,
    CONF_ID,
    CONF_MICROPHONE,
    CONF_PORT,
    CONF_SPEAKER,
    PLATFORM_ESP32,
)
from esphome.types import ConfigType

# socket and ring_buffer are not pulled in by microphone or speaker.
AUTO_LOAD = ["audio", "ring_buffer", "socket"]
DEPENDENCIES = ["microphone", "network"]
CODEOWNERS = ["@Teagan42"]

CONF_DUCKING_SPEAKER = "ducking_speaker"
CONF_HOST = "host"
CONF_RECONNECT_INTERVAL = "reconnect_interval"

# Two sources on one mic: channel 0 fully processed, channel 1 the XMOS's
# second output (SPEC §3.2).
MAX_MICROPHONE_SOURCES = 2

chorus_bridge_ns = cg.esphome_ns.namespace("chorus_bridge")
ChorusBridge = chorus_bridge_ns.class_("ChorusBridge", cg.Component)


def _validate_distinct_channels(config: ConfigType) -> ConfigType:
    """Rejects two sources on the same channel: they would duplicate audio
    under one channel id and the host could not tell them apart."""
    seen: dict[int, None] = {}
    for source in config[CONF_MICROPHONE]:
        for channel in source[CONF_CHANNELS]:
            if channel in seen:
                raise cv.Invalid(f"microphone channel {channel} is used twice")
            seen[channel] = None
    return config


def _validate_one_microphone(config: ConfigType) -> ConfigType:
    """Rejects sources on different microphones. The contract is two sources
    over one Microphone, and only then is channel 0 the AEC'd peer of 1."""
    mics = {str(source[CONF_MICROPHONE]) for source in config[CONF_MICROPHONE]}
    if len(mics) > 1:
        raise cv.Invalid("all microphone sources must share one microphone")
    return config


CONFIG_SCHEMA = cv.All(
    cv.Schema(
        {
            cv.GenerateID(): cv.declare_id(ChorusBridge),
            # IP, not a hostname: ESPHome's set_sockaddr does not resolve.
            cv.Required(CONF_HOST): cv.ipaddress,
            cv.Required(CONF_PORT): cv.port,
            cv.Required(CONF_MICROPHONE): cv.All(
                cv.ensure_list(
                    microphone.microphone_source_schema(
                        min_bits_per_sample=16,
                        max_bits_per_sample=16,
                        min_channels=1,
                        max_channels=1,
                    )
                ),
                cv.Length(min=1, max=MAX_MICROPHONE_SOURCES),
            ),
            cv.Optional(CONF_SPEAKER): cv.use_id(speaker.Speaker),
            # apply_ducking lives on mixer_speaker::SourceSpeaker, not on the
            # Speaker base class, so ducking needs its own id.
            cv.Optional(CONF_DUCKING_SPEAKER): cv.use_id(SourceSpeaker),
            cv.Optional(
                CONF_RECONNECT_INTERVAL, default="5s"
            ): cv.positive_time_period_milliseconds,
        }
    ).extend(cv.COMPONENT_SCHEMA),
    cv.only_on([PLATFORM_ESP32]),
    _validate_distinct_channels,
    _validate_one_microphone,
)

FINAL_VALIDATE_SCHEMA = cv.Schema(
    {
        cv.Optional(CONF_MICROPHONE): cv.ensure_list(
            microphone.final_validate_microphone_source_schema(
                "chorus_bridge", sample_rate=16000
            )
        ),
    },
    extra=cv.ALLOW_EXTRA,
)


async def to_code(config: ConfigType) -> None:
    var = cg.new_Pvariable(config[CONF_ID])
    await cg.register_component(var, config)

    cg.add(var.set_host(str(config[CONF_HOST])))
    cg.add(var.set_port(config[CONF_PORT]))
    cg.add(var.set_reconnect_interval(config[CONF_RECONNECT_INTERVAL]))

    for source in config[CONF_MICROPHONE]:
        # passive=False is load-bearing: a passive source only receives audio
        # while another consumer has started the mic (SPEC §3.2).
        mic_source = await microphone.microphone_source_to_code(source, passive=False)
        cg.add(var.add_microphone_source(mic_source, source[CONF_CHANNELS][0]))

    # The Microphone itself, for observing authoritative hardware mute.
    mic = await cg.get_variable(config[CONF_MICROPHONE][0][CONF_MICROPHONE])
    cg.add(var.set_microphone(mic))

    if CONF_SPEAKER in config:
        spk = await cg.get_variable(config[CONF_SPEAKER])
        cg.add(var.set_speaker(spk))

    if CONF_DUCKING_SPEAKER in config:
        # Define-guarded so a satellite without a mixer does not compile
        # mixer_speaker.h, which is not a dependency of this component.
        cg.add_define("USE_CHORUS_BRIDGE_DUCKING")
        ducking = await cg.get_variable(config[CONF_DUCKING_SPEAKER])
        cg.add(var.set_ducking_speaker(ducking))
