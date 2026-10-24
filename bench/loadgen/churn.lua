-- Connection churn: every n-th request on a connection, n the script's
-- argument, asks the server to close it, so that another is opened, with its
-- own TLS handshake (no TLS session is resumed); report.lua prints the
-- summary.
dofile("/fitbench/report.lua")

local every, n = 1, 0
function init(args)
  every = tonumber(args[1]) or 1
end

request = function()
  n = n + 1
  if n % every == 0 then
    return wrk.format(nil, nil, {["Connection"] = "close"})
  end
  return wrk.format()
end
