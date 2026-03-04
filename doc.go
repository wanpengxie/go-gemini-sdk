// Package gemini provides a minimal ACP client SDK for Gemini CLI.
//
// The SDK starts a Gemini CLI subprocess and communicates with it via
// ACP (JSON-RPC 2.0 over stdio). It supports initialize handshake, session
// lifecycle, prompt sending, event receiving, permission callbacks, and
// graceful shutdown.
package gemini
