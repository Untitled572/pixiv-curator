CREATE INDEX artworks_trimmed_artist ON artworks(artist_id) WHERE retained_page_indices IS NOT NULL;
