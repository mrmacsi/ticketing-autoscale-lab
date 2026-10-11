package queue

import redis "github.com/redis/go-redis/v9"

// Lua scripts of the redis store. Every script gets the same KEYS (see redisKeyNames) and
// ARGV[1] = pub/sub channel, ARGV[2] = fencing value (” = no fencing: this instance always leads).
// Script-specific arguments start at ARGV[3]. Timestamps are Unix milliseconds computed by the caller
// (the room's clock), never Redis TIME, so tests can drive time.
//
// Redis runs one script at a time and blocks every other client meanwhile, so every script is
// O(batch), never O(line): a line of 10,000+ people must not make any single call slow. Reads of
// whole keys are limited to keys of bounded size (state, counters, capacity, signals, the 100-slot
// rate window and the 2,000-sample wait reservoir). Work proportional to the line (declaring sold out,
// dropping people who left, pruning finished entries) is done in batches of redisBatch per tick.
//
// Entry encoding in the entries hash: "<state>,<joinedMs>,<expMs>,<customerRef>" where state is
// w (waiting), a (admitted), r (released), x (expired) or s (sold out). customerRef is last because it
// may contain commas. While the sold-out flag is set, entries still in the line (being drained) are
// reported as sold out.
const luaPrelude = `
local K_CFG, K_SEQ, K_LINE, K_SEEN, K_ENT, K_CUST, K_ACT, K_FIN = KEYS[1], KEYS[2], KEYS[3], KEYS[4], KEYS[5], KEYS[6], KEYS[7], KEYS[8]
local K_CNT, K_ST, K_RATE, K_WAITS, K_SIG, K_CAP, K_CAPN, K_LEAD = KEYS[9], KEYS[10], KEYS[11], KEYS[12], KEYS[13], KEYS[14], KEYS[15], KEYS[16]
local CHAN, FENCE = ARGV[1], ARGV[2]
local BATCH, SAMPLES = 500, 2000
local function fenced() return FENCE ~= '' and redis.call('GET', K_LEAD) ~= FENCE end
local function fmt(n) return string.format('%d', n) end
local function enc(st, j, x, ref) return st .. ',' .. j .. ',' .. x .. ',' .. ref end
local function dec(v) return string.match(v, '^(%a),(%-?%d+),(%-?%d+),(.*)$') end
local function dropcust(id, ref)
  if ref ~= '' and redis.call('HGET', K_CUST, ref) == id then redis.call('HDEL', K_CUST, ref) end
end
-- Admissions in the last 10 s (100 slots of 100 ms). The leader stores the sum every tick; the hash
-- (at most 100 fields) is only summed here when that value is older than a second (no leader ticking).
local function ratecount(cur)
  local r = redis.call('HMGET', K_ST, 'rate', 'rateSlot')
  if r[2] and cur - tonumber(r[2]) <= 10 and cur >= tonumber(r[2]) then return tonumber(r[1]) end
  local t = redis.call('HGETALL', K_RATE)
  local n = 0
  for i = 1, #t, 2 do
    local d = cur - tonumber(t[i])
    if d >= 0 and d < 100 then n = n + tonumber(t[i + 1]) end
  end
  return n
end
-- finish sets the state of a batch of entries (nil keeps it), drops their customerRef mappings and,
-- with finAt, records them as finished: a fixed number of commands whatever the batch size.
local function finish(ids, newst, finAt)
  if #ids == 0 then return end
  local vals = redis.call('HMGET', K_ENT, unpack(ids))
  local sets, refs, owners = {}, {}, {}
  for i = 1, #ids do
    if vals[i] then
      local st, j, x, ref = dec(vals[i])
      if newst then
        sets[#sets + 1] = ids[i]
        sets[#sets + 1] = enc(newst, j, x, ref)
      end
      if ref ~= '' then
        refs[#refs + 1] = ref
        owners[#owners + 1] = ids[i]
      end
    end
  end
  if #sets > 0 then redis.call('HSET', K_ENT, unpack(sets)) end
  if #refs > 0 then
    local cur = redis.call('HMGET', K_CUST, unpack(refs))
    local del = {}
    for i = 1, #refs do
      if cur[i] == owners[i] then del[#del + 1] = refs[i] end
    end
    if #del > 0 then redis.call('HDEL', K_CUST, unpack(del)) end
  end
  if finAt then
    local z = {}
    for i = 1, #ids do
      z[#z + 1] = finAt
      z[#z + 1] = ids[i]
    end
    redis.call('ZADD', K_FIN, unpack(z))
  end
end
local function members(popped)
  local ids = {}
  for i = 1, #popped, 2 do ids[#ids + 1] = popped[i] end
  return ids
end
local function soldout() return redis.call('HGET', K_ST, 'soldOut') == '1' end
-- {id, state, position, waiting, expMs, admissions in the last 10 s, eventId}
local function view(id, st, x, cur)
  local so = soldout()
  if so and st == 'w' then st = 's' end
  local pos, rc, waiting = 0, 0, 0
  if not so then waiting = redis.call('ZCARD', K_LINE) end
  if st == 'w' then
    local r = redis.call('ZRANK', K_LINE, id)
    if r then pos = r + 1 end
    rc = ratecount(cur)
  end
  return {id, st, pos, waiting, x, rc, redis.call('HGET', K_CFG, 'event') or ''}
end
-- UNLINK frees big keys (a 10,000-entry line) in the background instead of blocking Redis.
local CLEAR = {K_SEQ, K_LINE, K_SEEN, K_ENT, K_CUST, K_ACT, K_FIN, K_CNT, K_ST, K_RATE, K_WAITS, K_SIG}
local function clear()
  for _, k in ipairs(CLEAR) do redis.call('UNLINK', k) end
  redis.call('HINCRBY', K_CFG, 'gen', 1)
  redis.call('PUBLISH', CHAN, '*')
end
`

// ARGV: 3 now, 4 new id, 5 eventId (lower case), 6 customerRef, 7 current 100 ms slot.
var luaJoin = redis.NewScript(luaPrelude + `
local c = redis.call('HMGET', K_CFG, 'enabled', 'event')
if c[1] ~= '1' then return {'open'} end
if string.lower(c[2] or '') ~= ARGV[5] then return {'unknown_event'} end
local now, id, ref, cur = ARGV[3], ARGV[4], ARGV[6], tonumber(ARGV[7])
local so = soldout()
if ref ~= '' then
  local old = redis.call('HGET', K_CUST, ref)
  if old then
    local v = redis.call('HGET', K_ENT, old)
    if v then
      local st, j, x = dec(v)
      -- A waiting entry of a sold-out room is sold out (its mapping is dropped when the line drains).
      if not (so and st == 'w') then return view(old, st, x, cur) end
    end
  end
end
redis.call('HINCRBY', K_CNT, 'joined', 1)
if so then
  redis.call('HSET', K_ENT, id, enc('s', now, '0', ref))
  redis.call('ZADD', K_FIN, now, id)
  redis.call('HINCRBY', K_CNT, 'soldOut', 1)
  return view(id, 's', '0', cur)
end
local seq = redis.call('INCR', K_SEQ)
redis.call('ZADD', K_LINE, seq, id)
redis.call('ZADD', K_SEEN, now, id)
redis.call('HSET', K_ENT, id, enc('w', now, '0', ref))
if ref ~= '' then redis.call('HSET', K_CUST, ref, id) end
return view(id, 'w', '0', cur)
`)

// ARGV: 3 now, 4 id, 5 touch ('1' records a check-in), 6 current slot.
var luaStatus = redis.NewScript(luaPrelude + `
local id = ARGV[4]
local v = redis.call('HGET', K_ENT, id)
if not v then return {'unknown'} end
local st, j, x = dec(v)
if ARGV[5] == '1' and st == 'w' and not soldout() then redis.call('ZADD', K_SEEN, ARGV[3], id) end
return view(id, st, x, tonumber(ARGV[6]))
`)

// ARGV: 3 now, 4 id, 5 outcome. Returns 1 when released (or already released), 0 otherwise.
var luaRelease = redis.NewScript(luaPrelude + `
local now, id = ARGV[3], ARGV[4]
local v = redis.call('HGET', K_ENT, id)
if not v then return 0 end
local st, j, x, ref = dec(v)
if st == 'a' then
  redis.call('HSET', K_ENT, id, enc('r', j, x, ref))
  redis.call('ZREM', K_ACT, id)
  redis.call('ZADD', K_FIN, now, id)
  dropcust(id, ref)
  redis.call('HINCRBY', K_CNT, 'released', 1)
  redis.call('HINCRBY', K_CNT, 'rel:' .. ARGV[5], 1)
  return 1
elseif st == 'w' and not soldout() then
  redis.call('ZREM', K_LINE, id)
  redis.call('ZREM', K_SEEN, id)
  redis.call('HDEL', K_ENT, id)
  dropcust(id, ref)
  redis.call('HINCRBY', K_CNT, 'leftWaiting', 1)
  redis.call('PUBLISH', CHAN, 'w ' .. id)
  return 1
elseif st == 'r' then
  return 1
end
return 0
`)

// The admission tick (leader only). ARGV: 3 now, 4 current slot, 5 random seed (1..2^31-2).
// Order as in the memory room: expire admissions, drop people who stopped checking in, declare sold
// out, admit from the head of the line, prune finished entries. Every step handles at most BATCH
// entries with a fixed number of multi-member commands; a step with more work left continues on the
// next tick (100 ms later). Returns
// {status, admitted}; status -1 means this instance is no longer the leader (nothing was done).
var luaTick = redis.NewScript(luaPrelude + `
if fenced() then return {-1, 0} end
local c = redis.call('HMGET', K_CFG, 'enabled', 'ttl', 'wto', 'grace', 'mode', 'per', 'start')
if c[1] ~= '1' then return {0, 0} end
local nows, now, cur = ARGV[3], tonumber(ARGV[3]), tonumber(ARGV[4])
local rnd = tonumber(ARGV[5])
local ttl, wto, grace = tonumber(c[2]) * 1000, tonumber(c[3]) * 1000, tonumber(c[4]) * 1000
local per, start = tonumber(c[6]), tonumber(c[7])
local woke = {}

-- 1. admissions past their TTL free their slot
local expired = redis.call('ZRANGEBYSCORE', K_ACT, '-inf', nows, 'LIMIT', 0, BATCH)
if #expired > 0 then
  finish(expired, 'x', nows)
  redis.call('ZREM', K_ACT, unpack(expired))
  redis.call('HINCRBY', K_CNT, 'expired', #expired)
end

local s = redis.call('HMGET', K_ST, 'lastSweep', 'soldOut', 'invKnown', 'invAvail', 'invHeld', 'zeroSince', 'budget', 'lastPrune')
local soldOut = s[2] == '1'

-- 2. people in line who stopped checking in are forgotten (once a second; a full batch continues
--    on the next tick, and admission waits until the backlog is gone so nobody who left is admitted)
local backlog = false
if wto > 0 and not soldOut and now - tonumber(s[1] or '0') >= 1000 then
  local gone = redis.call('ZRANGEBYSCORE', K_SEEN, '-inf', '(' .. fmt(now - wto), 'LIMIT', 0, BATCH)
  if #gone < BATCH then redis.call('HSET', K_ST, 'lastSweep', nows) else backlog = true end
  if #gone > 0 then
    finish(gone, nil, nil)
    redis.call('HDEL', K_ENT, unpack(gone))
    redis.call('ZREM', K_LINE, unpack(gone))
    redis.call('ZREM', K_SEEN, unpack(gone))
    for _, id in ipairs(gone) do woke[#woke + 1] = id end
    redis.call('HINCRBY', K_CNT, 'abandoned', #gone)
  end
end

-- 3. sold out: nothing available and nothing held (or the grace period passed without holds known).
--    Declaring is O(1): the flag makes everyone still in line sold out at once (status, join,
--    release and stats read it) and one '*' message wakes every long poll; the line itself is then
--    drained BATCH entries per tick.
local known = s[3] == '1'
local avail, held, zero = tonumber(s[4] or '0'), tonumber(s[5] or '-1'), tonumber(s[6] or '0')
local budget = tonumber(s[7] or '0')
if not soldOut and known and avail == 0 and zero > 0 and (held == 0 or (held < 0 and now - zero >= grace)) then
  soldOut = true
  redis.call('HSET', K_ST, 'soldOut', '1')
  local n = redis.call('ZCARD', K_LINE)
  if n > 0 then redis.call('HINCRBY', K_CNT, 'soldOut', n) end
  redis.call('PUBLISH', CHAN, '*')
end
if soldOut then
  local ids = members(redis.call('ZPOPMIN', K_LINE, BATCH))
  if #ids > 0 then
    finish(ids, 's', nows)
    redis.call('ZREM', K_SEEN, unpack(ids))
  end
end

-- 4. admit from the head of the line while active < capacity (and tickets are left), at most BATCH
--    per tick
local admitted = 0
if not soldOut and not backlog and (not known or budget > 0) then
  local healthy = tonumber(redis.call('HGET', K_CAP, 'healthy') or '0')
  local limit = per
  if c[5] == 'adaptive' then
    limit = tonumber(redis.call('HGET', K_ST, 'ctlLimit') or '0')
    if limit == 0 then
      limit = start
      redis.call('HSET', K_ST, 'ctlLimit', fmt(start), 'ctlPhase', 'slow_start', 'ctlDecision', 'hold', 'ctlReason', 'starting value')
    end
  end
  local capacity = healthy * limit
  local free = math.min(capacity - redis.call('ZCARD', K_ACT), BATCH)
  if known then free = math.min(free, budget) end
  local ids = {}
  if free > 0 then ids = members(redis.call('ZPOPMIN', K_LINE, free)) end
  if #ids > 0 then
    local expAt = fmt(now + ttl)
    local vals = redis.call('HMGET', K_ENT, unpack(ids))
    redis.call('ZREM', K_SEEN, unpack(ids))
    local sets, act, pushes = {}, {}, {}
    local cnt = redis.call('HMGET', K_CNT, 'waitCount', 'waitMax')
    local n, wmax = tonumber(cnt[1] or '0'), tonumber(cnt[2] or '0')
    for i = 1, #ids do
      if vals[i] then
        local st, j, x, ref = dec(vals[i])
        sets[#sets + 1] = ids[i]
        sets[#sets + 1] = enc('a', j, expAt, ref)
        act[#act + 1] = expAt
        act[#act + 1] = ids[i]
        admitted = admitted + 1
        woke[#woke + 1] = ids[i]
        -- join -> admitted wait: every sample up to SAMPLES, then a uniform reservoir of SAMPLES
        local w = now - tonumber(j)
        n = n + 1
        if w > wmax then wmax = w end
        if n <= SAMPLES then
          pushes[#pushes + 1] = fmt(w)
        else
          rnd = (rnd * 16807) % 2147483647
          local k = rnd % n
          if k < SAMPLES then redis.call('LSET', K_WAITS, k, fmt(w)) end
        end
      end
    end
    if #sets > 0 then
      redis.call('HSET', K_ENT, unpack(sets))
      redis.call('ZADD', K_ACT, unpack(act))
    end
    if #pushes > 0 then redis.call('RPUSH', K_WAITS, unpack(pushes)) end
    redis.call('HSET', K_CNT, 'waitCount', fmt(n), 'waitMax', fmt(wmax))
    if known then budget = budget - admitted end
  end
  if admitted > 0 then
    redis.call('HINCRBY', K_CNT, 'admitted', admitted)
    redis.call('HINCRBY', K_RATE, fmt(cur), admitted)
    if known then redis.call('HSET', K_ST, 'budget', fmt(budget)) end
  end
end

-- Admission rate over the last 10 s: drop slots older than the window (at most 100 remain) and
-- store the sum for status calls.
local rt = redis.call('HGETALL', K_RATE)
local rc = 0
for i = 1, #rt, 2 do
  local d = cur - tonumber(rt[i])
  if d >= 100 then redis.call('HDEL', K_RATE, rt[i]) elseif d >= 0 then rc = rc + tonumber(rt[i + 1]) end
end
redis.call('HSET', K_ST, 'rate', fmt(rc), 'rateSlot', fmt(cur))

-- 5. finished entries answer by id for an hour, then are forgotten (checked once a minute; a full
--    batch continues on the next tick)
if now - tonumber(s[8] or '0') >= 60000 then
  local old = redis.call('ZRANGEBYSCORE', K_FIN, '-inf', '(' .. fmt(now - 3600000), 'LIMIT', 0, BATCH)
  if #old < BATCH then redis.call('HSET', K_ST, 'lastPrune', nows) end
  if #old > 0 then
    redis.call('HDEL', K_ENT, unpack(old))
    redis.call('ZREM', K_FIN, unpack(old))
  end
end

if #woke > 0 then redis.call('PUBLISH', CHAN, 'w ' .. table.concat(woke, ' ')) end
return {1, admitted}
`)

// ARGV: 3 config JSON, 4 enabled ('1'/'0'), 5 eventId, 6 activeUsersPerServer, 7 admissionTtlSeconds,
// 8 soldOutGraceSeconds, 9 admissionMode, 10 waitingTimeoutSeconds, 11 adaptive starting limit.
// A different eventId or enabled=false clears the room. Returns 1 when cleared.
var luaConfigure = redis.NewScript(luaPrelude + `
local old = redis.call('HGET', K_CFG, 'event') or ''
local cleared = ARGV[4] ~= '1' or old ~= ARGV[5]
redis.call('HSET', K_CFG, 'json', ARGV[3], 'enabled', ARGV[4], 'event', ARGV[5], 'per', ARGV[6], 'ttl', ARGV[7],
  'grace', ARGV[8], 'mode', ARGV[9], 'wto', ARGV[10], 'start', ARGV[11])
if cleared then clear() else redis.call('PUBLISH', CHAN, 'c') end
return cleared and 1 or 0
`)

var luaReset = redis.NewScript(luaPrelude + `
clear()
return 1
`)

// ARGV: 3 healthy, 4 source, 5 last error, 6 servers JSON, 7.. healthy server names. Signals of
// servers that left the healthy set are dropped.
var luaSetCapacity = redis.NewScript(luaPrelude + `
if fenced() then return 0 end
redis.call('HSET', K_CAP, 'healthy', ARGV[3], 'source', ARGV[4], 'err', ARGV[5], 'servers', ARGV[6])
redis.call('DEL', K_CAPN)
local keep = {}
for i = 7, #ARGV do
  redis.call('SADD', K_CAPN, ARGV[i])
  keep[ARGV[i]] = true
end
for _, n in ipairs(redis.call('HKEYS', K_SIG)) do
  if not keep[n] then redis.call('HDEL', K_SIG, n) end
end
return 1
`)

// ARGV: 3 server name, 4 signals JSON. Ignored for servers that are not healthy.
var luaSetSignals = redis.NewScript(luaPrelude + `
if fenced() then return 0 end
if redis.call('SISMEMBER', K_CAPN, ARGV[3]) == 1 then
  redis.call('HSET', K_SIG, ARGV[3], ARGV[4])
  return 1
end
return 0
`)

// ARGV: 3 eventId, 4 available, 5 held (-1 unknown), 6 app sold out ('1'/'0'), 7 observed at.
var luaSetInventory = redis.NewScript(luaPrelude + `
if fenced() then return 0 end
local c = redis.call('HMGET', K_CFG, 'enabled', 'event')
if c[1] ~= '1' or c[2] ~= ARGV[3] then return 0 end
local zero = redis.call('HGET', K_ST, 'zeroSince') or '0'
if tonumber(ARGV[4]) > 0 then zero = '0' elseif zero == '0' then zero = ARGV[7] end
redis.call('HSET', K_ST, 'invKnown', '1', 'invAvail', ARGV[4], 'invHeld', ARGV[5], 'invAppSoldOut', ARGV[6],
  'invObservedAt', ARGV[7], 'invErr', '', 'budget', ARGV[4], 'zeroSince', zero)
return 1
`)

// ARGV: 3 eventId, 4 reason.
var luaSetInventoryUnknown = redis.NewScript(luaPrelude + `
if fenced() then return 0 end
if (redis.call('HGET', K_CFG, 'event') or '') ~= ARGV[3] then return 0 end
redis.call('HSET', K_ST, 'invKnown', '0', 'invErr', ARGV[4])
return 1
`)

// ARGV: 3 room generation the step was computed for, 4.. field/value pairs of the controller state.
var luaSetController = redis.NewScript(luaPrelude + `
if fenced() then return 0 end
if (redis.call('HGET', K_CFG, 'gen') or '0') ~= ARGV[3] then return 0 end
for i = 4, #ARGV, 2 do redis.call('HSET', K_ST, ARGV[i], ARGV[i + 1]) end
return 1
`)

// Everything Stats and Control need, read atomically. Every read is O(1) or of a bounded key; the
// wait samples (at most SAMPLES) only when ARGV[3] = '1'.
var luaSnapshot = redis.NewScript(luaPrelude + `
local oldest, waiting = 0, 0
if not soldout() then
  waiting = redis.call('ZCARD', K_LINE)
  local first = redis.call('ZRANGE', K_LINE, 0, 0)
  if first[1] then
    local v = redis.call('HGET', K_ENT, first[1])
    if v then
      local st, j = dec(v)
      oldest = tonumber(j)
    end
  end
end
local waits = {}
if ARGV[3] == '1' then waits = redis.call('LRANGE', K_WAITS, 0, SAMPLES - 1) end
return {redis.call('HGET', K_CFG, 'json') or '', redis.call('HGET', K_CFG, 'gen') or '0',
  redis.call('HGETALL', K_ST), redis.call('HGETALL', K_CAP), redis.call('HGETALL', K_CNT),
  waiting, redis.call('ZCARD', K_ACT), oldest, redis.call('HGETALL', K_RATE),
  redis.call('HGETALL', K_SIG), waits, redis.call('GET', K_LEAD) or ''}
`)

// Leader lease: renew only while we still hold it; resign only our own lease.
var luaRenew = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('PEXPIRE', KEYS[1], ARGV[2]) end
return 0
`)

var luaResign = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)
