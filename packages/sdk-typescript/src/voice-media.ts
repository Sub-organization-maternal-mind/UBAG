import type { UbagIceServer, UbagVoiceSessionConnectResponse } from "./types.js";

// Browser-side WebRTC helper for a live voice session. It performs the
// single-request offer/answer exchange the gateway uses for signaling, applies
// the returned ice_servers, opens the `control` data channel and authenticates
// it with the session-scoped media_credential.
//
// SECURITY: the UBAG API key must never reach the browser. `exchange` should
// call YOUR backend, which calls client.connectVoiceSession(...) with the key
// held server-side and relays the connect response back (see
// examples/javascript/voice-browser.html and voice-backend.mjs).
//
// The peer-connection types below are structural so the helper is unit-testable
// in Node with a fake; in a browser RTCPeerConnection satisfies them.

export interface VoiceDataChannelLike {
  readyState: string;
  send(data: string): void;
  close(): void;
  addEventListener(type: string, listener: (event: any) => void): void;
}

export interface VoicePeerConnectionLike {
  readonly iceGatheringState: string;
  readonly localDescription: { sdp?: string | undefined } | null;
  createDataChannel(label: string): VoiceDataChannelLike;
  addTrack(track: unknown, ...streams: unknown[]): unknown;
  createOffer(): Promise<{ sdp?: string | undefined }>;
  setLocalDescription(description: any): Promise<void>;
  setRemoteDescription(description: { type: "answer"; sdp: string }): Promise<void>;
  getConfiguration?(): object;
  setConfiguration?(configuration: any): void;
  addEventListener(type: string, listener: (event: any) => void): void;
  removeEventListener(type: string, listener: (event: any) => void): void;
  close(): void;
}

export interface VoiceMicrophoneLike {
  getTracks(): unknown[];
}

/** Events the gateway sends on the control channel. */
export type VoiceControlEvent =
  | { event: "status"; state: string; muted?: boolean }
  | { event: "auth"; ok: boolean }
  | { event: "mute"; muted: boolean }
  | { event: "pong" }
  | { event: "error"; reason: string };

export interface VoiceMediaClientOptions {
  /** Relays the SDP offer to your backend -> connectVoiceSession; returns the connect response. */
  exchange: (sdpOffer: string) => Promise<UbagVoiceSessionConnectResponse>;
  /** Microphone stream (getUserMedia result); its tracks are added to the connection. */
  stream?: VoiceMicrophoneLike;
  /** Initial ICE servers (for example public STUN) used before the connect response arrives. */
  iceServers?: UbagIceServer[];
  /** Defaults to `new RTCPeerConnection(config)`. Inject a fake in tests. */
  createPeerConnection?: (config: { iceServers: UbagIceServer[] }) => VoicePeerConnectionLike;
  /** Called for each control-channel event and for remote `track` events. */
  onEvent?: (event: VoiceControlEvent) => void;
  onTrack?: (event: unknown) => void;
  /** Max wait for ICE gathering before sending the offer (default 5000). */
  iceGatheringTimeoutMs?: number;
}

const CONTROL_LABEL = "control";

export class VoiceMediaClient {
  /** Resolves when the gateway accepts the control credential; rejects if it does not. */
  ready: Promise<void> = Promise.resolve();

  private pc: VoicePeerConnectionLike | undefined;
  private channel: VoiceDataChannelLike | undefined;
  private tracks: unknown[] = [];
  private authenticated = false;
  private closed = false;

  constructor(private readonly options: VoiceMediaClientOptions) {}

  get isAuthenticated(): boolean {
    return this.authenticated;
  }

  async connect(): Promise<UbagVoiceSessionConnectResponse> {
    if (this.pc !== undefined) {
      throw new Error("VoiceMediaClient.connect() was already called; create a new client.");
    }
    const factory = this.options.createPeerConnection ?? defaultPeerConnection;
    const pc = factory({ iceServers: this.options.iceServers ?? [] });
    this.pc = pc;

    let settleReady!: { resolve: () => void; reject: (error: Error) => void };
    this.ready = new Promise<void>((resolve, reject) => {
      settleReady = { resolve, reject };
    });
    this.ready.catch(() => undefined); // callers may never await it

    try {
      this.tracks = this.options.stream?.getTracks() ?? [];
      for (const track of this.tracks) {
        pc.addTrack(track, this.options.stream);
      }
      if (this.options.onTrack) {
        pc.addEventListener("track", this.options.onTrack);
      }

      // The channel must exist before the offer so it is negotiated in it.
      const channel = pc.createDataChannel(CONTROL_LABEL);
      this.channel = channel;
      channel.addEventListener("message", (message) => this.handleMessage(message.data, settleReady));

      const offer = await pc.createOffer();
      await pc.setLocalDescription(offer);
      await waitForIceGathering(pc, this.options.iceGatheringTimeoutMs ?? 5000);
      const sdpOffer = pc.localDescription?.sdp ?? offer.sdp;
      if (!sdpOffer) {
        throw new Error("The peer connection produced no SDP offer.");
      }

      const response = await this.options.exchange(sdpOffer);
      if (!response.sdp_answer) {
        throw new Error("The connect response has no sdp_answer.");
      }
      if (response.ice_servers && response.ice_servers.length > 0 && pc.setConfiguration) {
        pc.setConfiguration({ ...(pc.getConfiguration?.() ?? {}), iceServers: response.ice_servers });
      }
      await pc.setRemoteDescription({ type: "answer", sdp: response.sdp_answer });

      const credential = response.media_credential;
      if (!credential) {
        throw new Error("The connect response has no media_credential.");
      }
      const sendAuth = () => channel.send(JSON.stringify({ op: "auth", credential }));
      if (channel.readyState === "open") {
        sendAuth();
      } else {
        channel.addEventListener("open", sendAuth);
      }
      return response;
    } catch (error) {
      settleReady.reject(error instanceof Error ? error : new Error(String(error)));
      this.close();
      throw error;
    }
  }

  /** Mutes/unmutes the microphone: locally (track.enabled) and on the gateway control channel. */
  mute(muted: boolean): void {
    this.sendControl({ op: "mute", muted });
    for (const track of this.tracks) {
      (track as { enabled?: boolean }).enabled = !muted;
    }
  }

  ping(): void {
    this.sendControl({ op: "ping" });
  }

  /** Closes the control channel and peer connection. Terminate the session server-side too. */
  close(): void {
    if (this.closed) {
      return;
    }
    this.closed = true;
    this.authenticated = false;
    try {
      this.channel?.close();
    } finally {
      this.pc?.close();
    }
  }

  private sendControl(command: Record<string, unknown>): void {
    if (!this.channel || this.channel.readyState !== "open") {
      throw new Error("The voice control channel is not open.");
    }
    if (!this.authenticated) {
      throw new Error("The voice control channel is not authenticated yet; await ready first.");
    }
    this.channel.send(JSON.stringify(command));
  }

  private handleMessage(data: unknown, ready: { resolve: () => void; reject: (error: Error) => void }): void {
    let event: VoiceControlEvent;
    try {
      event = JSON.parse(String(data)) as VoiceControlEvent;
    } catch {
      return;
    }
    if (event.event === "auth") {
      this.authenticated = event.ok === true;
      if (this.authenticated) {
        ready.resolve();
      } else {
        ready.reject(new Error("The gateway rejected the media credential."));
      }
    }
    this.options.onEvent?.(event);
  }
}

function defaultPeerConnection(config: { iceServers: UbagIceServer[] }): VoicePeerConnectionLike {
  const Ctor = (globalThis as unknown as { RTCPeerConnection?: new (config: unknown) => VoicePeerConnectionLike }).RTCPeerConnection;
  if (!Ctor) {
    throw new Error("RTCPeerConnection is not available; run in a browser or pass createPeerConnection.");
  }
  return new Ctor(config);
}

// The gateway signals over one HTTP request, so the offer must carry its ICE
// candidates: wait for gathering to finish (bounded) before sending it.
function waitForIceGathering(pc: VoicePeerConnectionLike, timeoutMs: number): Promise<void> {
  if (pc.iceGatheringState === "complete") {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const done = () => {
      clearTimeout(timer);
      pc.removeEventListener("icegatheringstatechange", onChange);
      resolve();
    };
    const onChange = () => {
      if (pc.iceGatheringState === "complete") {
        done();
      }
    };
    const timer = setTimeout(done, timeoutMs);
    pc.addEventListener("icegatheringstatechange", onChange);
  });
}
