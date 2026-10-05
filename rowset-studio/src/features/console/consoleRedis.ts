// redis-cli syntax, translated into the three key operations Rowset exposes:
// scan, write and delete. Everything the user types still goes through those
// endpoints, so a console command is governed and audited exactly as the
// same operation from the editor is.

export type RedisPlan =
  | { kind: "scan"; pattern: string; type?: string; limit?: number; /** Only these columns are printed. */ columns?: string[] }
  | { kind: "write"; key: string; type: "string" | "hash"; field?: string; value: string; ttlSeconds?: number }
  | { kind: "delete"; key: string }
  | { kind: "error"; message: string };

/** Splits a command line the way redis-cli does, honouring quoted values. */
export function tokens(line: string): string[] {
  const out: string[] = [];
  let current = "";
  let quote: string | null = null;
  let started = false;
  for (let index = 0; index < line.length; index++) {
    const character = line[index];
    if (quote) {
      if (character === "\\" && index + 1 < line.length) {
        current += line[++index];
        continue;
      }
      if (character === quote) {
        quote = null;
        continue;
      }
      current += character;
      continue;
    }
    if (character === '"' || character === "'") {
      quote = character;
      started = true;
      continue;
    }
    if (/\s/.test(character)) {
      if (started || current) out.push(current);
      current = "";
      started = false;
      continue;
    }
    current += character;
    started = true;
  }
  if (started || current) out.push(current);
  return out;
}

const supported = "KEYS, SCAN, GET, MGET, TYPE, TTL, SET, SETEX, HSET, HGET, DEL and UNLINK";

export function redisPlan(line: string): RedisPlan {
  const parts = tokens(line);
  if (!parts.length) return { kind: "error", message: "Type a command." };
  const command = parts[0].toUpperCase();
  const argument = (index: number) => parts[index] ?? "";
  switch (command) {
    // SCAN's cursor and COUNT are the server's paging; the scan endpoint owns
    // both, so only MATCH, TYPE and a row cap carry over.
    case "KEYS":
    case "SCAN": {
      const rest = parts.slice(1);
      let pattern = command === "KEYS" ? argument(1) || "*" : "*";
      let type: string | undefined;
      let limit: number | undefined;
      for (let index = command === "KEYS" ? 1 : 0; index < rest.length; index++) {
        const word = rest[index].toUpperCase();
        if (word === "MATCH") pattern = rest[++index] ?? "*";
        else if (word === "TYPE") type = rest[++index];
        else if (word === "COUNT") limit = Number(rest[++index]) || undefined;
      }
      return { kind: "scan", pattern, type, limit };
    }
    case "GET":
      if (!argument(1)) return { kind: "error", message: "GET needs a key." };
      return { kind: "scan", pattern: argument(1), columns: ["key", "value"] };
    case "MGET": {
      const keys = parts.slice(1);
      if (!keys.length) return { kind: "error", message: "MGET needs at least one key." };
      // One scan covers them all only when a single pattern can; otherwise
      // the braces form a pattern the server understands as an alternation.
      return { kind: "scan", pattern: keys.length === 1 ? keys[0] : `{${keys.join(",")}}`, columns: ["key", "value"] };
    }
    case "TYPE":
      if (!argument(1)) return { kind: "error", message: "TYPE needs a key." };
      return { kind: "scan", pattern: argument(1), columns: ["key", "type"] };
    case "TTL":
    case "PTTL":
      if (!argument(1)) return { kind: "error", message: `${command} needs a key.` };
      return { kind: "scan", pattern: argument(1), columns: ["key", "ttl"] };
    case "HGET":
      if (!argument(1)) return { kind: "error", message: "HGET needs a key." };
      return { kind: "scan", pattern: argument(1), columns: ["key", "value"] };
    case "SET":
    case "SETEX": {
      const key = argument(1);
      // SETEX puts the seconds before the value; SET takes EX after it.
      const value = command === "SETEX" ? argument(3) : argument(2);
      if (!key || value === "") return { kind: "error", message: `${command} needs a key and a value.` };
      let ttlSeconds: number | undefined = command === "SETEX" ? Number(argument(2)) || undefined : undefined;
      for (let index = 3; index < parts.length; index++) {
        if (parts[index].toUpperCase() === "EX") ttlSeconds = Number(parts[index + 1]) || undefined;
      }
      return { kind: "write", key, type: "string", value, ttlSeconds };
    }
    case "HSET": {
      const [, key, field, value] = parts;
      if (!key || !field || value === undefined) return { kind: "error", message: "HSET needs a key, a field and a value." };
      return { kind: "write", key, type: "hash", field, value };
    }
    case "DEL":
    case "UNLINK":
      if (!argument(1)) return { kind: "error", message: `${command} needs a key.` };
      if (parts.length > 2) return { kind: "error", message: `${command} deletes one key at a time here, so each deletion is backed up and audited on its own.` };
      return { kind: "delete", key: argument(1) };
    default:
      return { kind: "error", message: `${command} is not available in this console. It understands ${supported}.` };
  }
}
