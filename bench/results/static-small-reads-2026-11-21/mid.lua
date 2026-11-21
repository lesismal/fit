-- 64 KiB files at random: 200 directories of 100 under /m.
local counter = 0
function setup(thread)
  thread:set("id", counter)
  counter = counter + 1
end
function init(args)
  math.randomseed(2000 + id)
end
function request()
  return wrk.format("GET", string.format("/m/d%04d/f%04d", math.random(0, 199), math.random(0, 99)))
end
