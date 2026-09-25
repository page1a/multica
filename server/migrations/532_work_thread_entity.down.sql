DROP TRIGGER IF EXISTS sync_work_thread_from_task ON agent_task_queue;
DROP FUNCTION IF EXISTS sync_work_thread_from_task();
DROP TABLE IF EXISTS work_thread;
