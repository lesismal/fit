-- Asks for /cache/1 to /cache/100000, the n-th most popular path as often as
-- 1/n, as Zipf's law with s = 1 has it, for fitbench's cache-zipf scenario;
-- report.lua prints the summary.
dofile("/fitbench/report.lua")

local keys = 100000
local cdf = {}
local sum = 0
for i = 1, keys do
  sum = sum + 1 / i
  cdf[i] = sum
end
for i = 1, keys do
  cdf[i] = cdf[i] / sum
end

local threads = 0
function setup(thread)
  threads = threads + 1
  thread:set("id", threads)
end

function init(args)
  math.randomseed(os.time() * 1000 + (id or 0))
end

request = function()
  local r = math.random()
  local lo, hi = 1, keys
  while lo < hi do
    local mid = math.floor((lo + hi) / 2)
    if cdf[mid] < r then lo = mid + 1 else hi = mid end
  end
  return wrk.format("GET", "/cache/" .. lo)
end
