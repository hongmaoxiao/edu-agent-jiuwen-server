-- 防重复提交：客户端每次确认生成带一个 request_id，网络重试复用同一个；同一账号同一 request_id 只建一个任务、只扣一次点。
ALTER TABLE comic_tasks ADD COLUMN request_id text;
CREATE UNIQUE INDEX comic_tasks_request ON comic_tasks(account_id, request_id) WHERE request_id IS NOT NULL;
