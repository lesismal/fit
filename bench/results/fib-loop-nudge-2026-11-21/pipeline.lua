-- Sixteen requests for / sent at once on each connection, as a pipelining
-- client sends them.
init = function(args)
  local r = {}
  for i = 1, 16 do
    r[i] = wrk.format(nil, "/")
  end
  req = table.concat(r)
end

request = function()
  return req
end
