-- The API workload: four of five requests a GET of /api/item, and the fifth
-- a POST of 2 KiB to it, with an API client's headers, which fitbench writes
-- to /fitbench/data/headers.lua; report.lua prints the summary.
dofile("/fitbench/report.lua")
dofile("/fitbench/data/headers.lua")

local post_headers = {}
for k, v in pairs(api_headers) do post_headers[k] = v end
post_headers["Content-Type"] = "application/json"

local n = 0
request = function()
  n = n + 1
  if n % 5 == 0 then
    return wrk.format("POST", "/api/item", post_headers, api_post_body)
  end
  return wrk.format("GET", "/api/item", api_headers)
end
