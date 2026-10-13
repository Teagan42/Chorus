// Enrollment takes recorded in the browser (cmd/reviewui/pages/voices.tmpl).
// The server takes only the satellite's own format and resamples nothing, so
// each take is decoded, rendered down to 16 kHz mono and written as 16-bit
// PCM WAV here, then sent in the form's file input beside any files picked.
// Without a microphone, MediaRecorder or a secure context, the recorder stays
// hidden and the form is an upload.
"use strict";

(() => {
  const RATE = 16000;

  const form = document.querySelector("form[data-min]");
  const box = form && form.querySelector("[data-recorder]");
  if (!box) return;
  const ok = window.isSecureContext && navigator.mediaDevices?.getUserMedia &&
    window.MediaRecorder && window.DataTransfer && window.OfflineAudioContext;
  if (!ok) return;

  const input = form.querySelector("input[type=file]");
  const button = box.querySelector("[data-record]");
  const status = box.querySelector("[data-status]");
  const list = box.querySelector("[data-takes]");
  const min = Number(form.dataset.min) || 3;
  let picked = [];
  const recorded = [];
  let recorder = null;

  // The input carries what was picked and what was recorded, in that order.
  function sync() {
    const dt = new DataTransfer();
    for (const f of [...picked, ...recorded]) dt.items.add(f);
    input.files = dt.files;
    const n = dt.files.length;
    status.textContent = n >= min ? `${n} takes ready.` : `${n} of at least ${min} takes.`;
  }

  input.addEventListener("change", () => {
    picked = [...input.files].filter((f) => !recorded.includes(f));
    sync();
  });

  function wav(samples) {
    const b = new DataView(new ArrayBuffer(44 + samples.length * 2));
    const str = (o, s) => [...s].forEach((c, i) => b.setUint8(o + i, c.charCodeAt(0)));
    str(0, "RIFF"); b.setUint32(4, 36 + samples.length * 2, true); str(8, "WAVE");
    str(12, "fmt "); b.setUint32(16, 16, true); b.setUint16(20, 1, true); b.setUint16(22, 1, true);
    b.setUint32(24, RATE, true); b.setUint32(28, RATE * 2, true); b.setUint16(32, 2, true); b.setUint16(34, 16, true);
    str(36, "data"); b.setUint32(40, samples.length * 2, true);
    samples.forEach((x, i) => b.setInt16(44 + i * 2, Math.max(-1, Math.min(1, x)) * 0x7fff, true));
    return new Blob([b], { type: "audio/wav" });
  }

  // decode renders whatever the browser recorded to 16 kHz mono.
  async function decode(blob) {
    const ctx = new AudioContext();
    try {
      const audio = await ctx.decodeAudioData(await blob.arrayBuffer());
      const off = new OfflineAudioContext(1, Math.ceil(audio.duration * RATE), RATE);
      const src = off.createBufferSource();
      src.buffer = audio;
      src.connect(off.destination);
      src.start();
      return (await off.startRendering()).getChannelData(0);
    } finally {
      ctx.close();
    }
  }

  function add(file, seconds) {
    recorded.push(file);
    const li = document.createElement("li");
    li.textContent = `${file.name} · ${seconds.toFixed(1)} s `;
    const drop = document.createElement("button");
    drop.type = "button";
    drop.className = "btn";
    drop.textContent = "Drop";
    drop.addEventListener("click", () => {
      recorded.splice(recorded.indexOf(file), 1);
      li.remove();
      sync();
    });
    li.append(drop);
    list.append(li);
    sync();
  }

  async function start() {
    let stream;
    try {
      // The satellite sends its own front end's output; the browser's echo
      // cancelling and gain riding would only make this take less like it.
      stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: false, noiseSuppression: false, autoGainControl: false },
      });
    } catch (err) {
      status.textContent = `The microphone did not open: ${err.message}`;
      return;
    }
    const chunks = [];
    recorder = new MediaRecorder(stream);
    recorder.addEventListener("dataavailable", (e) => chunks.push(e.data));
    recorder.addEventListener("stop", async () => {
      stream.getTracks().forEach((t) => t.stop());
      recorder = null;
      button.textContent = "Record a take";
      try {
        const samples = await decode(new Blob(chunks, { type: chunks[0]?.type }));
        const seconds = samples.length / RATE;
        if (seconds < 1) {
          status.textContent = "That take was under a second; read the whole phrase.";
          return;
        }
        add(new File([wav(samples)], `take-${recorded.length + 1}.wav`, { type: "audio/wav" }), seconds);
      } catch (err) {
        status.textContent = `That take did not decode: ${err.message}`;
      }
    });
    recorder.start();
    button.textContent = "Stop";
    status.textContent = "Recording… read the next phrase, then press Stop.";
  }

  button.addEventListener("click", () => (recorder ? recorder.stop() : start()));
  box.hidden = false;
  sync();
})();
