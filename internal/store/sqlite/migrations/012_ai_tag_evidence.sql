-- Preserve existing catalog while applying explicit positive AI-label evidence.
UPDATE artworks SET ai_status='ai' WHERE ai_status<>'ai' AND EXISTS (
 SELECT 1 FROM artwork_tags at JOIN tags t ON t.id=at.tag_id
 WHERE at.illust_id=artworks.id AND replace(lower(trim(t.normalized_name)),'ａｉ','ai') IN
 ('ai生成','aiイラスト','ai绘画','ai繪畫','ai绘图','ai繪圖','ai-generated','ai generated','ai-generated art','aiart','ai artwork','ai画像','ai絵')
);
