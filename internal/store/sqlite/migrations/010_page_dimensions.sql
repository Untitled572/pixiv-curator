UPDATE image_pages SET width=0,height=0 WHERE page_index>0 AND (width<>0 OR height<>0);
