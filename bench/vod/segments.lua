-- Asks for the paths listed in the file named by the script's argument, one
-- a line, picked at random, each as often: the segments of a library, as many
-- players seeking about it would ask for them. report.lua prints the summary.
dofile("/fitbench/report.lua")

local paths = {}

local threads = 0
function setup(thread)
  threads = threads + 1
  thread:set("id", threads)
end

function init(args)
  for line in io.lines(args[1]) do
    if line ~= "" then
      paths[#paths + 1] = line
    end
  end
  math.randomseed(os.time() * 1000 + (id or 0))
end

request = function()
  return wrk.format("GET", paths[math.random(#paths)])
end
