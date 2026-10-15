# Temporal durability demo (Day 11)

What happened, on the Mac mini with `temporal server start-dev`:

1. Started worker 1 and a workflow that reads 4 files one per turn, then summarizes.
2. After 2 tool calls had finished, killed worker 1 with `kill -9` while a model
   call (activity 29) was in flight.
3. `temporal workflow describe` showed the workflow still **Running**, with 1 pending activity.
4. Started worker 2. Temporal noticed that worker 1's heartbeats had stopped and
   retried activity 29 on worker 2. Earlier steps were **not** re-run: their
   results came from the workflow history.
5. DeepSeek returned real `503 Service is too busy` errors on some attempts; the
   activity retry policy (backoff 2s, 4s, ...) handled them.
6. The workflow completed with the right answer.

```text
started workflow durability-demo-094448 (run 01a13d3b-c1b5-7d04-8e6a-eda1808edd55)
=== killed worker1 (pid 25759) at 09:44:52
Pending Activities: 1
  ActivityId          29
=== restarting worker at 09:44:56
=== done at 09:45:31
  Status          COMPLETED
```

Worker logs (trimmed):

```text
worker1  INFO model  ActivityID 5  CallModel Attempt 1
worker1  INFO tool   step 1 name read_file (main.go)
worker1  INFO model  ActivityID 17 CallModel Attempt 1
worker1  INFO tool   step 2 name read_file (tools.go)
         -- kill -9 worker1 while ActivityID 29 (CallModel) is running --
worker2  ERROR CallModel Attempt 2: api 503: Service is too busy ...
worker2  INFO model  ActivityID 29 CallModel Attempt 3      <- picked up after the crash
worker2  INFO tool   step 3 name read_file (context.go)
worker2  INFO model  ActivityID 41 CallModel Attempt 1
worker2  INFO tool   step 4 name read_file (mcp.go)
worker2  ERROR CallModel Attempt 1: api 503 ...; Attempt 2: api 503 ...
worker2  INFO model  ActivityID 53 CallModel Attempt 3      <- final answer
```

Event history (`temporal workflow show`), times UTC. Note the 26s gap at
event 29 -> 30 while no worker was alive and the retries that followed:

```text
1 2026-10-15T01:44:48Z WorkflowExecutionStarted 
5 2026-10-15T01:44:48Z ActivityTaskScheduled 
6 2026-10-15T01:44:48Z ActivityTaskStarted 
7 2026-10-15T01:44:50Z ActivityTaskCompleted 
11 2026-10-15T01:44:50Z ActivityTaskScheduled 
12 2026-10-15T01:44:50Z ActivityTaskStarted 
13 2026-10-15T01:44:50Z ActivityTaskCompleted 
17 2026-10-15T01:44:50Z ActivityTaskScheduled 
18 2026-10-15T01:44:50Z ActivityTaskStarted 
19 2026-10-15T01:44:52Z ActivityTaskCompleted 
23 2026-10-15T01:44:52Z ActivityTaskScheduled 
24 2026-10-15T01:44:52Z ActivityTaskStarted 
25 2026-10-15T01:44:52Z ActivityTaskCompleted 
29 2026-10-15T01:44:52Z ActivityTaskScheduled 
30 2026-10-15T01:45:18Z ActivityTaskStarted 
31 2026-10-15T01:45:20Z ActivityTaskCompleted 
35 2026-10-15T01:45:20Z ActivityTaskScheduled 
36 2026-10-15T01:45:20Z ActivityTaskStarted 
37 2026-10-15T01:45:20Z ActivityTaskCompleted 
41 2026-10-15T01:45:20Z ActivityTaskScheduled 
42 2026-10-15T01:45:20Z ActivityTaskStarted 
43 2026-10-15T01:45:22Z ActivityTaskCompleted 
47 2026-10-15T01:45:22Z ActivityTaskScheduled 
48 2026-10-15T01:45:22Z ActivityTaskStarted 
49 2026-10-15T01:45:22Z ActivityTaskCompleted 
53 2026-10-15T01:45:22Z ActivityTaskScheduled 
54 2026-10-15T01:45:28Z ActivityTaskStarted 
55 2026-10-15T01:45:31Z ActivityTaskCompleted 
59 2026-10-15T01:45:31Z WorkflowExecutionCompleted
```

Final answer:

I read all four files. 

**Summary:** `main.go` runs the loop: it builds a message history, calls the model (with `toolDefs`), prints any text, executes each `tool_use` via `runTool`, appends the `tool_result`s, and repeats until the model stops asking for tools or `maxSteps` (20) is hit—with system-prompt rules telling the model to only report what it actually saw. `tools.go` exposes the built-in tools from `internal/tools` (read_file/write_file/run_shell) and forwards any `mcp__`-prefixed name to the MCP server, while `mcp.go` starts that server, does the `initialize`/`tools/list` handshake, adds its tools to `toolDefs`, and relays `tools/call` requests over JSON-RPC. `context.go` keeps the prompt small in two stages—stubbing old `tool_result` contents (keeping the `tool_use`/`tool_result` pairs intact) and, once prompt tokens exceed `contextBudget`, asking the model to summarize the middle of the history while preserving the first prompt, current request, recent tail, and a side list of already-read files.
