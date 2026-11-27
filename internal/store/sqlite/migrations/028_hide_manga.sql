UPDATE artworks SET visible=0 WHERE kind='manga' AND visible<>0;
UPDATE jobs SET cancel_requested=1,state='canceled',lease_until=0,lease_token=''
WHERE kind IN ('hydrate_artwork','repair_media') AND state IN ('pending','running','retry_wait','paused_auth')
  AND json_extract(payload_json,'$.illust_id') IN (SELECT id FROM artworks WHERE kind='manga');

UPDATE recommendations
SET sample_illust_ids_json=(
  SELECT json_group_array(j.value ORDER BY CAST(j.key AS INTEGER))
  FROM json_each(recommendations.sample_illust_ids_json) j
  WHERE NOT EXISTS(SELECT 1 FROM artworks w WHERE w.id=CAST(j.value AS INTEGER) AND w.kind='manga')
)
WHERE EXISTS(
  SELECT 1 FROM json_each(recommendations.sample_illust_ids_json) j
  JOIN artworks w ON w.id=CAST(j.value AS INTEGER) WHERE w.kind='manga'
);
