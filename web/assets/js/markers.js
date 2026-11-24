let markers = [];
let spiderMarkers = [];
let activeSpiderKey = null;
let activePopup = null;

const PROPERTIES = {
    569:   'Date of birth',
    570:   'Date of death',
    571:   'Inception',
    575:   'Time of discovery',
    576:   'Dissolution date',
    577:   'Publication date',
    580:   'Start time',
    582:   'End time',
    585:   'Point in time',
    729:   'Service entry',
    730:   'Service retirement',
    746:   'Date of disappearance',
    1191:  'Date of first performance',
    1249:  'Time of earliest written record',
    1319:  'Earliest date',
    1326:  'Latest date',
    1619:  'Date of official opening',
    2031:  'Work period start',
    2032:  'Work period end',
    2669:  'Discontinued date',
    2754:  'Production date',
    3999:  'Date of official closure',
    5204:  'Floruit',
    6949:  'Announcement date',
    7124:  'Date of first flight',
    7125:  'Date of latest flight',
    7588:  'Commissioning',
    7589:  'Decommissioning',
    9667:  'Combat start',
    10135: 'Combat end',
    625:   'Coordinate location',
    19:    'Place of birth',
    20:    'Place of death'
};

function closeActivePopup() {
    if (activePopup) {
        activePopup.remove();
        activePopup = null;
    }
}

function showExclusivePopup(popup, lngLat) {
    if (activePopup && activePopup !== popup) {
        activePopup.remove();
    }
    popup.setLngLat(lngLat).addTo(map);
    activePopup = popup;
}

function buildPopupHtml(item, label) {
    const itemType = item.type || 'L';
    const canRead = (itemType !== 'E' && itemType !== 'T') || Boolean(item.article_id);
    const entId = item.entity_id || 0;
    const artId = item.article_id || 0;
    const readBtnHtml = canRead ? `<button class="btn btn-sm btn-primary w-100 py-0 mt-2" onclick="openArticle(${entId}, ${artId})">Article</button>` : '';

    return `
        <div class="p-1">
            <div class="d-flex justify-content-between align-items-center mb-1">
                <span class="type-badge type-badge-${itemType}">${label}</span>
            </div>
            <h6 class="fw-bold mb-1">${item.title || 'Untitled'}</h6>
            <p class="small text-muted mb-0">${item.snippet || ''}</p>
            ${readBtnHtml}
        </div>
    `;
}

function updateSpiderLegs(features) {
    if (!map) return;
    const source = map.getSource('spider-legs-source');
    if (!source) return;
    source.setData({ type: 'FeatureCollection', features: features || [] });
}

function collapseSpider() {
    spiderMarkers.forEach(m => m?.marker?.remove());
    spiderMarkers = [];
    activeSpiderKey = null;
    updateSpiderLegs([]);
}

function spiderfyGroup(group) {
    collapseSpider();
    if (!map || !group || group.entries.length <= 1) return;

    const count = group.entries.length;
    const centerPt = map.project([group.longitude, group.latitude]);
    const legFeatures = [];
    activeSpiderKey = `${group.latitude.toFixed(4)},${group.longitude.toFixed(4)}`;

    group.entries.forEach(({ item, idx }, i) => {
        let r, theta;
        if (count <= 8) {
            r = 54;
            theta = (2 * Math.PI * i) / count - Math.PI / 2;
        } else {
            r = 38 + 9 * i;
            theta = i * 0.85;
        }

        const px = centerPt.x + r * Math.cos(theta);
        const py = centerPt.y + r * Math.sin(theta);
        const targetPos = map.unproject([px, py]);

        legFeatures.push({
            type: 'Feature',
            geometry: {
                type: 'LineString',
                coordinates: [
                    [group.longitude, group.latitude],
                    [targetPos.lng, targetPos.lat]
                ]
            }
        });

        const wrapper = document.createElement('div');
        wrapper.className = 'marker-wrapper';

        const itemType = item.type || 'L';
        const label = `${itemType}${idx + 1}`;
        const pin = document.createElement('div');
        pin.className = `custom-pin pin-${itemType}`;
        pin.innerHTML = `<span>${label}</span>`;
        wrapper.appendChild(pin);

        const popup = new maplibregl.Popup({ offset: [0, -32], closeOnClick: false })
            .setHTML(buildPopupHtml(item, label));

        popup.on('close', () => {
            if (activePopup === popup) activePopup = null;
        });

        const sMarker = new maplibregl.Marker({ element: wrapper, anchor: 'bottom' })
            .setLngLat([targetPos.lng, targetPos.lat])
            .addTo(map);

        wrapper.addEventListener('click', (e) => {
            e.stopPropagation();
            showExclusivePopup(popup, [targetPos.lng, targetPos.lat]);
            highlightCard(idx);
        });
        wrapper.addEventListener('mouseenter', () => highlightCard(idx));

        spiderMarkers.push({ idx, marker: sMarker, popup, lngLat: [targetPos.lng, targetPos.lat], el: wrapper });
    });

    updateSpiderLegs(legFeatures);
}

function clearMarkers() {
    closeActivePopup();
    collapseSpider();
    markers.forEach(m => m?.marker?.remove());
    markers = [];
}

function renderMarkers(items) {
    clearMarkers();
    if (!map) return;

    const groups = new Map();
    items.forEach((item, idx) => {
        const hasCoords = typeof item.latitude === 'number' && typeof item.longitude === 'number' && (item.latitude !== 0 || item.longitude !== 0);
        if (!hasCoords) return;
        const key = `${item.latitude.toFixed(4)},${item.longitude.toFixed(4)}`;
        if (!groups.has(key)) {
            groups.set(key, {
                latitude: item.latitude,
                longitude: item.longitude,
                entries: []
            });
        }
        groups.get(key).entries.push({ item, idx });
    });

    groups.forEach((group, key) => {
        const wrapper = document.createElement('div');
        const count = group.entries.length;
        const isCluster = count > 1;

        if (isCluster) {
            wrapper.className = 'marker-wrapper cluster-wrapper';
            const circle = document.createElement('div');
            circle.className = 'marker-cluster-circle';
            circle.innerText = count.toString();
            wrapper.appendChild(circle);
        } else {
            wrapper.className = 'marker-wrapper';
            const entry = group.entries[0];
            const firstType = entry.item.type || 'L';
            const label = `${firstType}${entry.idx + 1}`;
            const pin = document.createElement('div');
            pin.className = `custom-pin pin-${firstType}`;
            pin.innerHTML = `<span>${label}</span>`;
            wrapper.appendChild(pin);
        }

        const marker = new maplibregl.Marker({ element: wrapper, anchor: isCluster ? 'center' : 'bottom' })
            .setLngLat([group.longitude, group.latitude])
            .addTo(map);

        let popup = null;
        if (!isCluster) {
            const entry = group.entries[0];
            const firstType = entry.item.type || 'L';
            const label = `${firstType}${entry.idx + 1}`;

            popup = new maplibregl.Popup({ offset: [0, -32], closeOnClick: false })
                .setHTML(buildPopupHtml(entry.item, label));

            popup.on('close', () => {
                if (activePopup === popup) activePopup = null;
            });

            wrapper.addEventListener('click', (e) => {
                e.stopPropagation();
                showExclusivePopup(popup, [group.longitude, group.latitude]);
                highlightCard(entry.idx);
            });
        } else {
            wrapper.addEventListener('click', (e) => {
                e.stopPropagation();
                closeActivePopup();
                if (activeSpiderKey === key) {
                    collapseSpider();
                } else {
                    spiderfyGroup(group);
                }
            });
        }

        const indices = group.entries.map(e => e.idx);
        markers.push({ key, group, indices, marker, popup, lngLat: [group.longitude, group.latitude], el: wrapper });
    });
}

function flyToMarker(idx) {
    highlightCard(idx);
    const mObj = markers.find(m => m.indices.includes(idx));
    if (mObj && map) {
        map.flyTo({ center: mObj.marker.getLngLat(), zoom: Math.max(map.getZoom(), 8) });
        if (mObj.group && mObj.group.entries.length > 1) {
            spiderfyGroup(mObj.group);
            const sObj = spiderMarkers.find(m => m.idx === idx);
            if (sObj) {
                showExclusivePopup(sObj.popup, sObj.lngLat);
                sObj.el.classList.add('highlighted');
            }
        } else if (mObj.popup) {
            showExclusivePopup(mObj.popup, mObj.lngLat);
            highlightMarker(idx);
        }
    }
}

function highlightMarker(idx) {
    const sObj = spiderMarkers.find(m => m.idx === idx);
    if (sObj) {
        sObj.el.classList.add('highlighted');
        return;
    }
    markers.find(m => m.indices.includes(idx))?.el.classList.add('highlighted');
}

function unhighlightMarker(idx) {
    spiderMarkers.find(m => m.idx === idx)?.el.classList.remove('highlighted');
    markers.find(m => m.indices.includes(idx))?.el.classList.remove('highlighted');
}
