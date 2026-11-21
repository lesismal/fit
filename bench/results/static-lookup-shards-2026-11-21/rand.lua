-- Each thread draws files at random, with a seed of its own, over the whole set.
local counter = 0
function setup(thread)
  thread:set("id", counter)
  counter = counter + 1
end
function init(args)
  math.randomseed(1000 + id)
end
function request()
  return wrk.format("GET", string.format("/d%04d/f%04d", math.random(0, 2559), math.random(0, 819)))
end
