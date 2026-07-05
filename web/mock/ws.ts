// Minimal RFC 6455 WebSocket server for the mock BFF. The standalone mock has no
// dependencies (it type-strips the shared fixtures with plain Node), so rather
// than pull in `ws` we implement just enough of the protocol to bridge the
// browser's exec/logs/terminals panes: the opening handshake, masked client
// frames (text/binary/close/ping), and unmasked server frames. Fragmentation and
// control frames are handled; extensions are not (we never negotiate any).
//
// Usage:
//   import { acceptWebSocket } from "./ws.ts";
//   server.on("upgrade", (req, socket, head) => {
//     const ws = acceptWebSocket(req, socket, head);
//     if (!ws) return; // not a WebSocket request
//     ws.on("message", (data, isBinary) => { ... });
//     ws.send("hello");                 // text frame
//     ws.send(Buffer.from([0x1b]), true); // binary frame
//   });

import { createHash } from "node:crypto";
import { EventEmitter } from "node:events";

const GUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

// The node http/socket types are not available to type-stripping (no @types/node
// wired into this standalone script), so the transport is described structurally.
type Sink = {
  write(chunk: Buffer): void;
  end(): void;
  on(event: string, cb: (...args: any[]) => void): void;
};
type UpgradeReq = { headers: Record<string, string | undefined> };

interface DecodedFrame {
  fin: boolean;
  opcode: number;
  payload: Buffer;
  consumed: number;
}

// acceptWebSocket completes the upgrade handshake and returns a Conn, or null if
// the request is not a valid WebSocket upgrade (in which case the socket is left
// untouched for the caller to reject).
export function acceptWebSocket(req: UpgradeReq, socket: Sink, head?: Buffer): Conn | null {
  const key = req.headers["sec-websocket-key"];
  if ((req.headers.upgrade || "").toLowerCase() !== "websocket" || !key) {
    return null;
  }
  const accept = createHash("sha1")
    .update(key + GUID)
    .digest("base64");
  socket.write(
    Buffer.from(
      "HTTP/1.1 101 Switching Protocols\r\n" +
        "Upgrade: websocket\r\n" +
        "Connection: Upgrade\r\n" +
        `Sec-WebSocket-Accept: ${accept}\r\n\r\n`,
    ),
  );
  const conn = new Conn(socket);
  if (head && head.length) conn._feed(head);
  return conn;
}

// Conn is one accepted connection. Events: "message" (data, isBinary),
// "close", "error".
export class Conn extends EventEmitter {
  private socket: Sink;
  private _buf: Buffer;
  private _closed: boolean;
  private _fragOp: number;
  private _fragChunks: Buffer[];

  constructor(socket: Sink) {
    super();
    this.socket = socket;
    this._buf = Buffer.alloc(0);
    this._closed = false;
    // Accumulator for fragmented data messages.
    this._fragOp = 0;
    this._fragChunks = [];

    socket.on("data", (chunk: Buffer) => this._feed(chunk));
    socket.on("close", () => this._finish());
    socket.on("error", (err: Error) => {
      this.emit("error", err);
      this._finish();
    });
  }

  _feed(chunk: Buffer): void {
    this._buf = this._buf.length ? Buffer.concat([this._buf, chunk]) : chunk;
    this._drain();
  }

  private _drain(): void {
    for (;;) {
      const frame = decodeFrame(this._buf);
      if (!frame) return; // need more bytes
      this._buf = this._buf.subarray(frame.consumed);
      this._handle(frame);
    }
  }

  private _handle(frame: DecodedFrame): void {
    const { fin, opcode, payload } = frame;
    switch (opcode) {
      case 0x0: // continuation
        this._fragChunks.push(payload);
        if (fin) this._deliver();
        break;
      case 0x1: // text
      case 0x2: // binary
        if (fin) {
          this.emit("message", payload, opcode === 0x2);
        } else {
          this._fragOp = opcode;
          this._fragChunks = [payload];
        }
        break;
      case 0x8: // close
        this.close();
        break;
      case 0x9: // ping -> pong (echo payload)
        this._write(0xa, payload);
        break;
      case 0xa: // pong: ignore
        break;
      default:
        break;
    }
  }

  private _deliver(): void {
    const data = Buffer.concat(this._fragChunks);
    const isBinary = this._fragOp === 0x2;
    this._fragChunks = [];
    this._fragOp = 0;
    this.emit("message", data, isBinary);
  }

  // send transmits a data frame. `binary` selects the binary opcode; strings are
  // always sent as text. Payload may be a string, Buffer, or Uint8Array.
  send(data: string | Buffer | Uint8Array, binary = false): void {
    if (this._closed) return;
    const buf = typeof data === "string" ? Buffer.from(data, "utf8") : Buffer.from(data);
    this._write(binary ? 0x2 : 0x1, buf);
  }

  private _write(opcode: number, payload: Buffer): void {
    if (this._closed) return;
    const len = payload.length;
    let header: Buffer;
    if (len < 126) {
      header = Buffer.from([0x80 | opcode, len]);
    } else if (len < 65536) {
      header = Buffer.from([0x80 | opcode, 126, (len >> 8) & 0xff, len & 0xff]);
    } else {
      header = Buffer.alloc(10);
      header[0] = 0x80 | opcode;
      header[1] = 127;
      // 64-bit length; JS numbers cover the low 32 bits we need here.
      header.writeUInt32BE(Math.floor(len / 2 ** 32), 2);
      header.writeUInt32BE(len >>> 0, 6);
    }
    try {
      this.socket.write(Buffer.concat([header, payload]));
    } catch {
      this._finish();
    }
  }

  close(code = 1000, reason = ""): void {
    if (this._closed) return;
    const body = Buffer.alloc(2 + Buffer.byteLength(reason));
    body.writeUInt16BE(code, 0);
    body.write(reason, 2);
    try {
      // Send a close frame directly, then end the socket.
      const header = Buffer.from([0x88, body.length]);
      this.socket.write(Buffer.concat([header, body]));
      this.socket.end();
    } catch {
      // ignore
    }
    this._finish();
  }

  private _finish(): void {
    if (this._closed) return;
    this._closed = true;
    this.emit("close");
  }
}

// decodeFrame parses one frame from the front of buf. Returns null if buf does
// not yet hold a complete frame. Client frames MUST be masked per RFC 6455.
function decodeFrame(buf: Buffer): DecodedFrame | null {
  if (buf.length < 2) return null;
  const b0 = buf[0];
  const b1 = buf[1];
  const fin = (b0 & 0x80) !== 0;
  const opcode = b0 & 0x0f;
  const masked = (b1 & 0x80) !== 0;
  let len = b1 & 0x7f;
  let offset = 2;

  if (len === 126) {
    if (buf.length < offset + 2) return null;
    len = buf.readUInt16BE(offset);
    offset += 2;
  } else if (len === 127) {
    if (buf.length < offset + 8) return null;
    const high = buf.readUInt32BE(offset);
    const low = buf.readUInt32BE(offset + 4);
    len = high * 2 ** 32 + low;
    offset += 8;
  }

  let maskKey: Buffer | undefined;
  if (masked) {
    if (buf.length < offset + 4) return null;
    maskKey = buf.subarray(offset, offset + 4);
    offset += 4;
  }

  if (buf.length < offset + len) return null;

  const payload = Buffer.from(buf.subarray(offset, offset + len));
  if (masked && maskKey) {
    for (let i = 0; i < payload.length; i++) payload[i] ^= maskKey[i & 3];
  }
  return { fin, opcode, payload, consumed: offset + len };
}
