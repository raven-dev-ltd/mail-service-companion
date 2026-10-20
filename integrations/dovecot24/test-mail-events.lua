-- Pure callback test: no real mailbox, filesystem, or notification involved.
local writes, errors, renamed = {}, 0, 0
dovecot = {i_error=function() errors=errors+1 end}
os.tmpname = function() return '/tmp/lua_fixture' end
os.remove = function() return true end
os.rename = function() renamed=renamed+1; return true end
io.open = function()
  return {write=function(_, data) table.insert(writes, data); return true end,
          close=function() return true end}
end
dofile(arg[1])
local ctx=dovecot_lua_notify_begin_txn({username='user@example.com'})
dovecot_lua_notify_event_message_new(ctx,{mailbox='Sent',uid=1,uid_validity=2})
dovecot_lua_notify_end_txn(ctx,true)
assert(#writes==0)
dovecot_lua_notify_event_message_new(ctx,{mailbox='INBOX',uid=3,uid_validity=2})
dovecot_lua_notify_end_txn(ctx,false)
assert(#writes==0)
dovecot_lua_notify_event_message_new(ctx,{mailbox='INBOX',uid=4.0,uid_validity=2.0})
dovecot_lua_notify_end_txn(ctx,true)
assert(#writes==1 and writes[1]:find('"lastUid":4') and renamed==1)
assert(not writes[1]:find('"lastUid":4%.0') and not writes[1]:find('"validity":2%.0'))
io.open=function() return nil end
dovecot_lua_notify_end_txn(ctx,true)
assert(errors==1)
print('Lua delivery hook tests passed')
