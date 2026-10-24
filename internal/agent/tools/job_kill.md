Terminate a background shell process.

<usage>
- Provide the shell ID returned from a background bash execution
- Cancels the running process and cleans up resources
</usage>

<features>
- Stop long-running background processes
- Clean up completed background shells
- Cancels the process and waits up to ten seconds for cleanup
- On Windows, terminates child processes in the command's Job Object
</features>

<tips>
- Use this when you need to stop a background process
- A successful result confirms the job has finished and removes its shell ID
- If cleanup times out, the tool returns an error and keeps the job tracked
- Use job_output to inspect a job whose termination is still pending
</tips>
