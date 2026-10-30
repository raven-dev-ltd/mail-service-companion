import test from "node:test"
import assert from "node:assert/strict"
import { readFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
import path from "node:path"

const root = path.resolve(import.meta.dirname, "..")
const compose = path.join(root, "compose.yml")
const example = path.join(root, ".env.example")
const dockerAvailable = spawnSync("docker", ["compose", "version"], { encoding: "utf8" }).status === 0

function configuration(port) {
  const env = { ...process.env }
  // Never read an operator's real .env or inherit its private IMAP settings.
  for (const key of Object.keys(env)) if (key.startsWith("COMPANION_")) delete env[key]
  if (port !== undefined) env.COMPANION_HTTP_PORT = port
  return spawnSync("docker", ["compose", "--env-file", example, "-f", compose, "config", "--format", "json"], { env, encoding: "utf8", timeout: 15_000 })
}

test("companion host port is env-configurable without changing internal listener or loopback isolation", () => {
  assert.match(readFileSync(compose, "utf8"), /127\.0\.0\.1:\$\{COMPANION_HTTP_PORT:-8080\}:8080/)
  assert.match(readFileSync(example, "utf8"), /^COMPANION_HTTP_PORT=8080$/m)
  assert.match(readFileSync(path.join(root, "Dockerfile"), "utf8"), /COMPANION_LISTEN=0\.0\.0\.0:8080/)
})

test("Compose resolves default, empty and custom host ports while preserving data", { skip: !dockerAvailable && "Docker Compose is not installed" }, () => {
  for (const [value, expected] of [[undefined, "8080"], ["", "8080"], ["8081", "8081"], ["18080", "18080"]]) {
    const result = configuration(value)
    assert.equal(result.status, 0, result.stderr)
    const service = JSON.parse(result.stdout).services.companion
    assert.equal(service.ports.length, 1)
    assert.equal(service.ports[0].host_ip, "127.0.0.1")
    assert.equal(String(service.ports[0].published), expected)
    assert.equal(service.ports[0].target, 8080)
    assert.equal(service.ports[0].protocol, "tcp")
    assert.equal(service.volumes[0].source, "companion-data")
    assert.equal(service.volumes[0].target, "/data")
    assert.equal(service.environment.COMPANION_HTTP_PORT, undefined)
  }
})

test("Compose rejects malformed or out-of-range host ports", { skip: !dockerAvailable && "Docker Compose is not installed" }, () => {
  for (const value of ["not-a-port", "70000"]) assert.notEqual(configuration(value).status, 0, `Accepted invalid host port ${value}`)
})
