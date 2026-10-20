-- Dovecot 2.4: local spool only; no HTTP, credentials, subjects or bodies.
local spool = "/tmp/docker-mailserver/companion-events"

local function quoted(value)
  return '"' .. value:gsub('[%z\1-\31\\"]', function(c)
    return string.format('\\u%04x', string.byte(c))
  end) .. '"'
end

function dovecot_lua_notify_begin_txn(user)
  return {owner=user.username, messages={}}
end

function dovecot_lua_notify_event_message_new(ctx, event)
  if event.mailbox == "INBOX" and event.uid > 0 and event.uid_validity > 0 then
    -- One cursor per transaction: the client synchronizes all intervening UIDs.
    ctx.messages[event.uid_validity] = math.max(ctx.messages[event.uid_validity] or 0, event.uid)
  end
end

function dovecot_lua_notify_end_txn(ctx, success)
  if not success then return end
  local ok = pcall(function()
    for validity, uid in pairs(ctx.messages) do
      local temporary = os.tmpname()
      local name = temporary:match('[^/]+$')
      os.remove(temporary)
      local target = spool .. '/' .. name
      local file = assert(io.open(target .. '.tmp', 'w'))
      -- Dovecot pushes UIDs as Lua floating-point numbers. tostring(11.0)
      -- produces a JSON float, which a strict uint32 event API must reject.
      local written = file:write('{"mailbox":' .. quoted(ctx.owner) .. ',"validity":' .. string.format('%.0f', validity) .. ',"lastUid":' .. string.format('%.0f', uid) .. '}')
      local closed = file:close()
      if not written or not closed then os.remove(target .. '.tmp'); error('spool write failed') end
      assert(os.rename(target .. '.tmp', target .. '.json'))
    end
  end)
  if not ok then dovecot.i_error('companion event spool failed; mail delivery is unaffected') end
end
