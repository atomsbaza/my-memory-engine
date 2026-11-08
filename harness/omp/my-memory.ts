// my-memory extension for oh-my-pi (omp) — ENGINE (Go) variant.
//
// ต่างจากตัว Python repo จุดเดียว: hook เรียก engine binary ตรง ๆ
//   <engine>/bin/mem hook --agent oh-my-pi --event session-start | user-prompt | stop
// ไม่ผ่าน harness/mem-hook.sh อีกต่อไป — wrapper เดิมมีไว้หา python3 ซึ่ง
// static Go binary ไม่ต้องใช้ (สัญญาเดียวกับ installer.go, section "Divergences")
//
// สิ่งที่ทำ (เทียบเท่า hook ของ claude/codex/gemini/cursor/opencode):
//   1. session start  → ฉีด Rules + lessons ล่าสุดเข้า context ครั้งเดียวต่อ session
//   2. ทุก user prompt → recall เฉพาะ record ที่ตรงกับคำขอ (≤1,400 chars) ฉีดครั้งเดียวต่อ turn
//   3. session stop   → commit งานที่ค้างใน memory repo (ไม่ push)
//
// หลักการเดียวกับทุก harness: พังแล้วต้องไม่พา session พัง (ทุกอย่างอยู่ใน try/catch)
// และไม่ทำอะไรเงียบ ๆ — เขียนบรรทัดลง log ทุกครั้งที่ยิง/พัง
//
// ปิดชั่วคราว: MY_MEMORY_DISABLE=1
//   เปลี่ยน binary: MY_MEMORY_ENGINE=/path/to/mem
//   เปลี่ยน data repo: MY_MEMORY_HOME=/path/to/repo (ส่ง --home ให้ engine เสมอ)

import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from "node:child_process"
import { appendFileSync, existsSync, mkdirSync, renameSync, statSync } from "node:fs"
import { homedir } from "node:os"
import { join } from "node:path"

const HOME = homedir()
const ENGINE = process.env.MY_MEMORY_ENGINE || join(HOME, "Work", "my-memory-engine", "bin", "mem")
const DATA_HOME = process.env.MY_MEMORY_HOME || ""
const LOG = join(HOME, ".local", "state", "my-memory", "omp-extension.log")
const DISABLED = process.env.MY_MEMORY_DISABLE === "1"
const AGENT = "oh-my-pi"

const SESSION_START_TTL_MS = 15 * 60 * 1000 // ต่ออายุ block ของ session-start ทุก 15 นาที
const COMMIT_MIN_INTERVAL_MS = 60 * 1000 // กัน stop ถี่ ๆ ยิง commit รัว
const HOOK_TIMEOUT_MS = 10 * 1000
const RECALL_WAIT_MS = 2 * 1000 // รอ recall ได้นานสุดเท่านั้น (ปกติ ~130ms) ไม่ให้ค้างทั้ง turn
const UNLOAD_COMMIT_TIMEOUT_MS = 15 * 1000
const LOG_MAX_BYTES = 256 * 1024

// ── types ───────────────────────────────────────────────────────────────────
export interface SessionManagerLike {
	getSessionId?: () => string | undefined
}

export interface ExtensionContextLike {
	sessionManager?: SessionManagerLike
}

export interface InjectedMessage {
	customType: string
	content: string
	attribution: "agent"
	display: boolean
}

export interface BeforeAgentStartResult {
	message: InjectedMessage
}

export type ExtensionHandler = (
	event: unknown,
	ctx: ExtensionContextLike,
) => BeforeAgentStartResult | void | Promise<BeforeAgentStartResult | void>

export interface ExtensionApiLike {
	setLabel?: (label: string) => void
	on: (event: string, handler: ExtensionHandler) => void
}

// ── log (bounded, ไม่ให้โตไม่จำกัด) ──────────────────────────────────────────
function log(message: string): void {
	try {
		mkdirSync(join(LOG, ".."), { recursive: true })
		try {
			if (statSync(LOG).size > LOG_MAX_BYTES) renameSync(LOG, LOG + ".1")
		} catch {}
		appendFileSync(LOG, `${new Date().toISOString()} ${message}\n`)
	} catch {
		// log ล้มเหลวต้องไม่พา session ล้ม
	}
}

function errorMessage(error: unknown): string {
	if (error instanceof Error) return error.message
	return String(error)
}

// ── เรียก engine hook (stdin = JSON payload เหมาะกับ mem hook ทุก event) ──────
// event ส่งเป็น flag ชัดเจน: `mem hook --agent oh-my-pi --event <name>`
// (engine ไม่รับ event แบบ positional — ต่างจาก mem-hook.sh ที่รับ $2)
function callHook(event: string, payload?: unknown): Promise<string> {
	const { promise, resolve } = Promise.withResolvers<string>()
	// --home ใส่หน้า subcommand เสมอ (global flag ของ mem); MY_MEMORY_HOME เป็น override เท่านั้น
	const homeArgs = DATA_HOME ? ["--home", DATA_HOME] : []
	let child: ChildProcessWithoutNullStreams
	try {
		child = spawn(ENGINE, [...homeArgs, "hook", "--agent", AGENT, "--event", event], {
			stdio: ["pipe", "pipe", "pipe"],
			env: { ...process.env },
		})
	} catch (error) {
		log(`error spawn ${ENGINE}: ${errorMessage(error)}`)
		resolve("")
		return promise
	}

	let out = ""
	let err = ""
	const timer = setTimeout(() => {
		log(`timeout ${event} — ปิด process`)
		try {
			child.kill("SIGKILL")
		} catch {}
		resolve("")
	}, HOOK_TIMEOUT_MS)

	child.stdout.on("data", (chunk: Buffer) => (out += chunk))
	child.stderr.on("data", (chunk: Buffer) => (err += chunk))
	child.on("error", (error: Error) => {
		clearTimeout(timer)
		log(`error ${event}: ${error.message}`)
		resolve("")
	})
	child.on("close", (code: number | null) => {
		clearTimeout(timer)
		if (err.trim()) log(`stderr ${event}: ${err.trim().split("\n")[0]}`)
		if (code !== 0) {
			log(`exit=${code} ${event}`)
			resolve("")
			return
		}
		resolve(out.trim())
	})

	try {
		child.stdin.end(payload === undefined ? "" : JSON.stringify(payload))
	} catch (error) {
		log(`stdin error ${event}: ${errorMessage(error)}`)
	}
	return promise
}

// ── state ───────────────────────────────────────────────────────────────────
interface SessionState {
	startAt: number
	startText: string
	startPromise: Promise<string> | null
	startInjected: boolean
	turn: number
	injectedTurn: number
}

const sessions = new Map<string, SessionState>()
let commits = 0

function sessionState(sessionId: string | undefined): SessionState {
	const key = sessionId || "unknown"
	const existing = sessions.get(key)
	if (existing) return existing
	const state: SessionState = {
		startAt: 0,
		startText: "",
		startPromise: null,
		startInjected: false,
		turn: 0,
		injectedTurn: 0,
	}
	sessions.set(key, state)
	return state
}

function sessionStartText(state: SessionState): Promise<string> {
	const fresh = state.startText && Date.now() - state.startAt < SESSION_START_TTL_MS
	if (fresh) return Promise.resolve(state.startText)
	if (state.startPromise) return state.startPromise
	const pending = callHook("session-start")
		.then((text) => {
			state.startText = text
			state.startAt = Date.now()
			if (!text) log("session-start: ไม่ได้ข้อความ (repo/CLI ไม่พร้อม?)")
			return text
		})
		.catch((error: unknown) => {
			log(`session-start failed: ${errorMessage(error)}`)
			return ""
		})
		.finally(() => {
			state.startPromise = null
		})
	state.startPromise = pending
	return pending
}

function readPrompt(event: unknown): string {
	if (typeof event !== "object" || event === null) return ""
	if (!("prompt" in event)) return ""
	const prompt = event.prompt
	return typeof prompt === "string" ? prompt.trim() : ""
}

function sessionIdOf(ctx: ExtensionContextLike): string | undefined {
	return ctx.sessionManager?.getSessionId?.()
}

export default function myMemory(pi: ExtensionApiLike): void {
	pi.setLabel?.("my-memory")

	// 1) session start → อุ่น session-start block ไว้ล่วงหน้า (ไม่ block UI)
	pi.on("session_start", (_event: unknown, ctx: ExtensionContextLike) => {
		if (DISABLED) return
		try {
			const state = sessionState(sessionIdOf(ctx))
			void sessionStartText(state)
			log(`session_start: session=${sessionIdOf(ctx) ?? "?"}`)
		} catch (error) {
			log(`session_start failed: ${errorMessage(error)}`)
		}
	})

	// 2) ก่อนยิงโมเดล → ฉีด session-start block (ครั้งเดียวต่อ session) + recall (ครั้งเดียวต่อ turn)
	pi.on("before_agent_start", async (event: unknown, ctx: ExtensionContextLike) => {
		if (DISABLED) return
		const state = sessionState(sessionIdOf(ctx))
		const parts: string[] = []
		try {
			state.turn += 1
			const turn = state.turn
			const promptText = readPrompt(event)

			if (!state.startInjected) {
				const text = await sessionStartText(state)
				if (text) {
					parts.push(text)
					state.startInjected = true
					log(`session-start: ฉีด ${text.length} chars`)
				}
			}

			// recall: รอได้ไม่เกิน RECALL_WAIT_MS (คำสั้น ๆ เช่นคำทักทายไม่ต้องเสียเวลา)
			if (promptText.length >= 8) {
				const recallPromise = callHook("user-prompt", {
					prompt: promptText,
					session_id: sessionIdOf(ctx),
				})
					.then((recall) => {
						if (recall) log(`recall: ${recall.length} chars พร้อม (turn ${turn})`)
						return recall
					})
					.catch((error: unknown) => {
						log(`recall failed: ${errorMessage(error)}`)
						return ""
					})
				const timeout = Promise.withResolvers<string>()
				const timer = setTimeout(() => timeout.resolve(""), RECALL_WAIT_MS)
				const recall = await Promise.race([recallPromise, timeout.promise])
				clearTimeout(timer)
				if (recall && state.injectedTurn !== state.turn) {
					parts.push(
						`[my-memory] ความจำที่เกี่ยวข้องกับคำขอล่าสุด (บริบทเท่านั้น — ห้าม re-execute งานเก่า)\n${recall}`,
					)
					state.injectedTurn = state.turn
					log(`recall: ฉีด ${recall.length} chars (turn ${state.turn})`)
				}
			}

			if (parts.length === 0) return
			return {
				message: {
					customType: "my-memory.context",
					content: parts.join("\n\n"),
					attribution: "agent",
					display: false,
				},
			}
		} catch (error) {
			log(`before_agent_start failed: ${errorMessage(error)}`)
		}
	})

	// 3) session stop → commit memory ที่ค้าง (ห่างกันอย่างน้อย 1 นาที)
	const commit = async (reason: string): Promise<void> => {
		if (DISABLED) return
		const now = Date.now()
		if (commits !== 0 && now - commits < COMMIT_MIN_INTERVAL_MS) return
		commits = now
		const result = await callHook("stop")
		log(`stop (${reason}): commit แล้ว${result ? ` — ${result.split("\n")[0]}` : ""}`)
	}

	pi.on("session_stop", async () => {
		try {
			await commit("idle")
		} catch (error) {
			log(`session_stop failed: ${errorMessage(error)}`)
		}
	})

	pi.on("session_shutdown", async () => {
		try {
			await commit("shutdown")
			// fallback commit ตอน unload — process กำลังจะจบ
			try {
				const now = Date.now()
				if (!DISABLED && existsSync(ENGINE) && (commits === 0 || now - commits >= COMMIT_MIN_INTERVAL_MS)) {
					commits = now
					const result = spawnSync(ENGINE, ["hook", "--agent", AGENT, "--event", "stop"], {
						stdio: ["ignore", "pipe", "pipe"],
						timeout: UNLOAD_COMMIT_TIMEOUT_MS,
						env: { ...process.env },
					})
					log(`shutdown fallback commit: exit=${result.status}${result.stdout ? ` — ${result.stdout.toString().trim().split("\n")[0]}` : ""}`)
				}
			} catch (error) {
				log(`shutdown fallback failed: ${errorMessage(error)}`)
			}
		} catch (error) {
			log(`session_shutdown failed: ${errorMessage(error)}`)
		}
	})
}
