-- Random files of one size class: ND directories of 1000 files under PREFIX.
local counter = 0
function setup(thread)
  thread:set("id", counter)
  counter = counter + 1
end
function init(args)
  math.randomseed(3000 + id)
  nd = tonumber(os.getenv("ND"))
  prefix = os.getenv("PREFIX")
end
function request()
  return wrk.format("GET", string.format("%s/d%04d/f%04d", prefix, math.random(0, nd - 1), math.random(0, 999)))
end
