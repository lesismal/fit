-- The website workload: the files listed in /fitbench/data/site-files.txt,
-- most popular first, the n-th asked for as often as 1/n, as Zipf's law with
-- s = 1 has it, under the prefix the script is given (/site/ or /edge/), with
-- a browser's headers; report.lua prints the summary.
dofile("/fitbench/report.lua")
dofile("/fitbench/data/headers.lua")

local files = {}
for line in io.lines("/fitbench/data/site-files.txt") do
  files[#files + 1] = line
end
local cdf = {}
local sum = 0
for i = 1, #files do
  sum = sum + 1 / i
  cdf[i] = sum
end
for i = 1, #files do
  cdf[i] = cdf[i] / sum
end

local threads = 0
function setup(thread)
  threads = threads + 1
  thread:set("id", threads)
end

local prefix = "/site/"
function init(args)
  prefix = args[1] or prefix
  math.randomseed(os.time() * 1000 + (id or 0))
end

request = function()
  local r = math.random()
  local lo, hi = 1, #files
  while lo < hi do
    local mid = math.floor((lo + hi) / 2)
    if cdf[mid] < r then lo = mid + 1 else hi = mid end
  end
  return wrk.format("GET", prefix .. files[lo], browser_headers)
end
