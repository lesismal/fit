-- The upload: a POST of n bytes, n the script's argument; report.lua prints
-- the summary.
dofile("/fitbench/report.lua")

local req
function init(args)
  local body = string.rep("u", tonumber(args[1]) or 1048576)
  req = wrk.format("POST", nil, {["Content-Type"] = "application/octet-stream"}, body)
end

request = function()
  return req
end
