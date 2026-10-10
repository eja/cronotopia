let map;
let markers = [];
let lastResults = [];
let isMobileMap = false;
let initialMapCenter = [12.4964, 41.9028];
let initialMapZoom = 4;

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

function getMapLayers() {
    if (typeof basemaps !== 'undefined' && typeof basemaps.layers === 'function') {
        const flavor = (typeof basemaps.namedFlavor === 'function') ? basemaps.namedFlavor("light") : "light";
        return basemaps.layers("protomaps", flavor, { lang: "en" });
    }
    return [
        { id: 'background', type: 'background', paint: { 'background-color': '#f2f3f4' } },
        { id: 'landcover', type: 'fill', source: 'protomaps', 'source-layer': 'landcover', paint: { 'fill-color': '#dce8d2' } },
        { id: 'water', type: 'fill', source: 'protomaps', 'source-layer': 'water', paint: { 'fill-color': '#a3c2e8' } },
        { id: 'roads', type: 'line', source: 'protomaps', 'source-layer': 'roads', paint: { 'line-color': '#ffffff', 'line-width': 1.5 } }
    ];
}

async function probeMaxZoom(lng, lat) {
    for (let z = 14; z >= 4; z--) {
        const x = Math.floor((lng + 180) / 360 * Math.pow(2, z));
        const latRad = lat * Math.PI / 180;
        const y = Math.floor((1 - Math.log(Math.tan(latRad) + 1 / Math.cos(latRad)) / Math.PI) / 2 * Math.pow(2, z));
        try {
            const resp = await fetch(`/tiles/${z}/${x}/${y}.pbf`);
            if (resp.ok) return z;
        } catch (_) {}
    }
    return 10;
}

async function initMap() {
    let mapConfig = { min_zoom: 0, max_zoom: 14, center_lng: 12.4964, center_lat: 41.9028, zoom: 4 };

    try {
        const res = await fetch('/api/map');
        if (res.ok) {
            const data = await res.json();
            if (data.max_zoom > 0) mapConfig.max_zoom = data.max_zoom;
            if (data.min_zoom !== undefined) mapConfig.min_zoom = data.min_zoom;
            if (data.center_lng && data.center_lat) {
                mapConfig.center_lng = data.center_lng;
                mapConfig.center_lat = data.center_lat;
                mapConfig.zoom = data.zoom || Math.max(mapConfig.min_zoom, 4);
            }
        } else {
            mapConfig.max_zoom = await probeMaxZoom(mapConfig.center_lng, mapConfig.center_lat);
        }
    } catch (e) {
        mapConfig.max_zoom = await probeMaxZoom(mapConfig.center_lng, mapConfig.center_lat);
    }

    initialMapCenter = [mapConfig.center_lng, mapConfig.center_lat];
    initialMapZoom = mapConfig.zoom;

    map = new maplibregl.Map({
        container: "map",
        style: {
            version: 8,
            sprite: window.location.origin + "/pics/light",
            sources: {
                "protomaps": {
                    type: "vector",
                    tiles: [window.location.origin + "/tiles/{z}/{x}/{y}.pbf"],
                    minzoom: mapConfig.min_zoom,
                    maxzoom: mapConfig.max_zoom
                }
            },
            layers: getMapLayers()
        },
        center: [mapConfig.center_lng, mapConfig.center_lat],
        zoom: mapConfig.zoom,
        minZoom: mapConfig.min_zoom,
        maxZoom: 22
    });

    map.addControl(new maplibregl.GeolocateControl({ positionOptions: { enableHighAccuracy: true }, trackUserLocation: true, showUserHeading: true }));
    map.addControl(new maplibregl.NavigationControl(), 'bottom-right');
    map.addControl(new maplibregl.ScaleControl({ unit: 'metric' }), 'bottom-left');
    map.on('load', () => map.resize());
}

function haversineDistance(lat1, lon1, lat2, lon2) {
    const R = 6371.0;
    const dLat = (lat2 - lat1) * Math.PI / 180.0;
    const dLon = (lon2 - lon1) * Math.PI / 180.0;
    const a = Math.sin(dLat/2) ** 2 + Math.cos(lat1 * Math.PI / 180.0) * Math.cos(lat2 * Math.PI / 180.0) * Math.sin(dLon/2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

function getMapRadiusKm() {
    const center = map.getCenter();
    const ne = map.getBounds().getNorthEast();
    return Math.max(1, Math.round(haversineDistance(center.lat, center.lng, ne.lat, ne.lng)));
}

function resetMap() {
    markers.forEach(m => m?.marker?.remove());
    markers = [];
    if (map) map.flyTo({ center: initialMapCenter, zoom: initialMapZoom });
}

async function executeSearch() {
    const query = document.getElementById('input-query').value.trim();
    const year = document.getElementById('input-year').value.trim();
    const month = document.getElementById('input-month').value.trim();
    const day = document.getElementById('input-day').value.trim();
    const range = document.getElementById('select-range').value;
    const mode = document.getElementById('select-mode').value;
    const restrictMap = document.getElementById('select-visible-map').value === 'true';

    if (!query && !year && !month && !day && !restrictMap) return;

    resetMap();

    const countEl = document.getElementById('results-count');
    const metaEl = document.getElementById('results-meta');
    const listEl = document.getElementById('results-list');
    const placeholder = document.getElementById('results-placeholder');

    listEl.innerHTML = '';
    placeholder.style.display = 'block';
    placeholder.innerHTML = '<div class="text-center py-5"><div class="spinner-border text-primary" role="status"></div><div class="mt-2 text-muted small">Searching...</div></div>';
    countEl.textContent = 'Searching...';
    metaEl.textContent = '...';

    const params = new URLSearchParams();
    if (query) params.set('query', query);
    if (year) params.set('year', year);
    if (month) params.set('month', month);
    if (day) params.set('day', day);
    if (range && range !== '0') params.set('range', range);
    if (mode) params.set('mode', mode);
    params.set('limit', '50');

    if (restrictMap && map) {
        const center = map.getCenter();
        params.set('latitude', center.lat.toFixed(6));
        params.set('longitude', center.lng.toFixed(6));
        params.set('radius', getMapRadiusKm().toString());
    }

    try {
        const t0 = performance.now();
        const resp = await fetch('/api?' + params.toString());
        if (!resp.ok) throw new Error('HTTP ' + resp.status);
        const data = await resp.json();
        const elapsed = ((performance.now() - t0) / 1000).toFixed(2);

        lastResults = data || [];
        countEl.textContent = `Found ${lastResults.length} in ${elapsed}s`;
        metaEl.textContent = `${lastResults.length} items`;

        renderResults(lastResults);
        renderMarkers(lastResults);
    } catch (err) {
        countEl.textContent = 'Error during search';
        metaEl.textContent = '0 items';
        placeholder.innerHTML = '<p class="text-danger py-5">An error occurred while fetching results.</p>';
    }
}

function renderResults(items) {
    const listEl = document.getElementById('results-list');
    const placeholder = document.getElementById('results-placeholder');

    listEl.innerHTML = '';
    if (!items || items.length === 0) {
        placeholder.style.display = 'block';
        placeholder.innerHTML = '<p class="text-muted py-5">No results found for your criteria.</p>';
        return;
    }
    placeholder.style.display = 'none';

    items.forEach((item, idx) => {
        const card = document.createElement('div');
        const itemType = item.type || 'C';
        card.className = `card result-card type-${itemType} p-2 shadow-sm`;
        card.dataset.index = idx;

        const typeConfigs = {
            'E': { char: 'E', title: 'Event' },
            'C': { char: 'L', title: 'Lexical match' },
            'V': { char: 'S', title: 'Semantic match' }
        };
        const currentType = typeConfigs[itemType] || { char: itemType, title: 'Item' };
        const tBadge = `<span class="type-badge type-badge-${itemType}" title="${currentType.title}">${currentType.char}</span>`;

        let times = Array.isArray(item.times) ? [...item.times] : [];
        if (times.length === 0 && (item.date_begin || item.date_end || item.year)) {
            times.push({
                code: item.code || 585,
                year: item.year || 0,
                date: item.date_begin || item.date_end || (item.year ? String(item.year) : '')
            });
        }

        let places = Array.isArray(item.places) ? [...item.places] : [];
        if (places.length === 0 && typeof item.latitude === 'number' && typeof item.longitude === 'number' && (item.latitude !== 0 || item.longitude !== 0)) {
            places.push({
                code: item.code || 625,
                latitude: item.latitude,
                longitude: item.longitude
            });
        }

        let timeBtnHtml = '';
        let timePopupHtml = '';
        if (times.length > 0) {
            timeBtnHtml = `<button type="button" class="btn btn-sm btn-panel-toggle btn-time-toggle" data-target="time-box-${idx}">Time</button>`;
            let timeItemsHtml = '';
            times.forEach(t => {
                const prop = PROPERTIES[t.code] || `Event P${t.code}`;
                const val = t.date || (t.year > 0 ? String(t.year) : `${Math.abs(t.year)} BCE`);
                const y = t.year || '';
                const m = t.month || '';
                const d = t.day || '';
                timeItemsHtml += `
                    <li class="events-list-item">
                        <span class="text-muted">${prop}</span>
                        <button type="button" class="btn-time-link" data-year="${y}" data-month="${m}" data-day="${d}">
                            ${val}
                        </button>
                    </li>
                `;
            });
            timePopupHtml = `
                <div id="time-box-${idx}" class="dropdown-popup-box time-dropdown-box mt-2" style="display: none;">
                    <ol class="events-list mb-0 ps-3">${timeItemsHtml}</ol>
                </div>
            `;
        }

        let spaceBtnHtml = '';
        let spacePopupHtml = '';
        if (places.length > 0) {
            spaceBtnHtml = `<button type="button" class="btn btn-sm btn-panel-toggle btn-space-toggle" data-target="space-box-${idx}">Space</button>`;
            let spaceItemsHtml = '';
            places.forEach(p => {
                const prop = PROPERTIES[p.code] || `Place P${p.code}`;
                spaceItemsHtml += `
                    <li class="events-list-item">
                        <span class="text-muted">${prop}</span>
                        <button type="button" class="btn-coord-link" data-lat="${p.latitude}" data-lon="${p.longitude}">
                            ${p.latitude.toFixed(4)}, ${p.longitude.toFixed(4)}
                        </button>
                    </li>
                `;
            });
            spacePopupHtml = `
                <div id="space-box-${idx}" class="dropdown-popup-box space-dropdown-box mt-2" style="display: none;">
                    <ol class="events-list mb-0 ps-3">${spaceItemsHtml}</ol>
                </div>
            `;
        }

        const canRead = itemType !== 'E' || Boolean(item.article_id);
        const readBtnHtml = canRead ? `<button class="btn btn-sm btn-outline-primary py-0 px-2 btn-read" style="font-size: 0.78rem;">Article</button>` : '';

        card.innerHTML = `
            <div class="d-flex justify-content-between align-items-start mb-1">
                <div class="fw-bold text-truncate me-2" title="${item.title || 'Untitled'}">${item.title || 'Untitled'}</div>
                <div>${tBadge}</div>
            </div>
            <div class="text-muted small mb-2 text-truncate-2" style="font-size: 0.85rem;">
                ${item.snippet || item.text || ''}
            </div>
            <div class="d-flex justify-content-between align-items-center mt-auto pt-1">
                <div class="d-flex align-items-center flex-wrap gap-1">
                    ${timeBtnHtml}
                    ${spaceBtnHtml}
                </div>
                ${readBtnHtml}
            </div>
            ${timePopupHtml}
            ${spacePopupHtml}
        `;

        card.addEventListener('click', (ev) => {
            const timeToggle = ev.target.closest('.btn-time-toggle');
            if (timeToggle) {
                ev.stopPropagation();
                const targetId = timeToggle.getAttribute('data-target');
                const box = document.getElementById(targetId);
                const spaceBox = card.querySelector('.space-dropdown-box');
                const spaceBtn = card.querySelector('.btn-space-toggle');
                if (spaceBox) spaceBox.style.display = 'none';
                if (spaceBtn) spaceBtn.classList.remove('active');
                if (box) {
                    const willShow = box.style.display === 'none';
                    box.style.display = willShow ? 'block' : 'none';
                    timeToggle.classList.toggle('active', willShow);
                }
                return;
            }

            const spaceToggle = ev.target.closest('.btn-space-toggle');
            if (spaceToggle) {
                ev.stopPropagation();
                const targetId = spaceToggle.getAttribute('data-target');
                const box = document.getElementById(targetId);
                const timeBox = card.querySelector('.time-dropdown-box');
                const timeBtn = card.querySelector('.btn-time-toggle');
                if (timeBox) timeBox.style.display = 'none';
                if (timeBtn) timeBtn.classList.remove('active');
                if (box) {
                    const willShow = box.style.display === 'none';
                    box.style.display = willShow ? 'block' : 'none';
                    spaceToggle.classList.toggle('active', willShow);
                }
                return;
            }

            const timeLink = ev.target.closest('.btn-time-link');
            if (timeLink) {
                ev.stopPropagation();
                const y = timeLink.getAttribute('data-year');
                const m = timeLink.getAttribute('data-month');
                const d = timeLink.getAttribute('data-day');
                document.getElementById('input-year').value = (y && y !== '0') ? y : '';
                document.getElementById('input-month').value = (m && m !== '0') ? m : '';
                document.getElementById('input-day').value = (d && d !== '0') ? d : '';
                return;
            }

            const coordBtn = ev.target.closest('.btn-coord-link');
            if (coordBtn) {
                ev.stopPropagation();
                const lat = parseFloat(coordBtn.getAttribute('data-lat'));
                const lon = parseFloat(coordBtn.getAttribute('data-lon'));
                if (!isNaN(lat) && !isNaN(lon) && map) {
                    map.flyTo({ center: [lon, lat], zoom: Math.max(map.getZoom(), 10) });
                }
                return;
            }

            if (ev.target.closest('.dropdown-popup-box')) {
                ev.stopPropagation();
                return;
            }

            if (ev.target.classList.contains('btn-read')) {
                ev.stopPropagation();
                openArticle(item.entity_id, item.article_id);
                return;
            }

            highlightCard(idx);
            highlightMarker(idx);
        });

        card.addEventListener('mouseenter', () => highlightMarker(idx));
        card.addEventListener('mouseleave', () => unhighlightMarker(idx));
        listEl.appendChild(card);
    });
}

function renderMarkers(items) {
    markers.forEach(m => m?.marker?.remove());
    markers = [];
    if (!map) return;

    items.forEach((item, idx) => {
        const hasCoords = typeof item.latitude === 'number' && typeof item.longitude === 'number' && (item.latitude !== 0 || item.longitude !== 0);
        if (!hasCoords) return;

        const wrapper = document.createElement('div');
        wrapper.className = 'marker-wrapper';

        const pin = document.createElement('div');
        const itemType = item.type || 'C';
        pin.className = `custom-pin pin-${itemType}`;
        pin.innerHTML = `<span>${idx + 1}</span>`;
        wrapper.appendChild(pin);

        const canRead = itemType !== 'E' || Boolean(item.article_id);
        const entId = item.entity_id || 0;
        const artId = item.article_id || 0;
        const readBtnHtml = canRead ? `<button class="btn btn-sm btn-primary w-100 py-0" onclick="openArticle(${entId}, ${artId})">Article</button>` : '';

        const popup = new maplibregl.Popup({ offset: [0, -32] }).setHTML(`
            <div class="p-1">
                <h6 class="fw-bold mb-1">${item.title || 'Untitled'}</h6>
                <p class="small text-muted mb-2">${item.snippet || ''}</p>
                ${readBtnHtml}
            </div>
        `);

        const marker = new maplibregl.Marker({ element: wrapper, anchor: 'bottom' })
            .setLngLat([item.longitude, item.latitude])
            .setPopup(popup)
            .addTo(map);

        marker.getElement().addEventListener('click', () => highlightCard(idx));
        markers.push({ idx, marker, el: wrapper });
    });
}

function flyToMarker(idx) {
    highlightCard(idx);
    highlightMarker(idx);
    const mObj = markers.find(m => m.idx === idx);
    if (mObj && map) {
        map.flyTo({ center: mObj.marker.getLngLat(), zoom: Math.max(map.getZoom(), 8) });
        mObj.marker.togglePopup();
    }
}

function highlightMarker(idx) {
    markers.find(m => m.idx === idx)?.el.classList.add('highlighted');
}

function unhighlightMarker(idx) {
    markers.find(m => m.idx === idx)?.el.classList.remove('highlighted');
}

function highlightCard(idx) {
    document.querySelectorAll('.result-card').forEach(c => c.classList.remove('active'));
    const card = document.querySelector(`.result-card[data-index="${idx}"]`);
    if (card) {
        card.classList.add('active');
        card.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
}

function setModalVisible(show) {
    const modalEl = document.getElementById('articleModal');
    let backdrop = document.getElementById('modal-backdrop');
    if (show) {
        modalEl.style.display = 'block';
        modalEl.removeAttribute('aria-hidden');
        modalEl.classList.add('show');
        document.body.classList.add('modal-open');
        if (!backdrop) {
            backdrop = document.createElement('div');
            backdrop.id = 'modal-backdrop';
            backdrop.className = 'modal-backdrop fade show';
            document.body.appendChild(backdrop);
        }
    } else {
        if (document.activeElement && modalEl.contains(document.activeElement)) document.activeElement.blur();
        modalEl.classList.remove('show');
        modalEl.style.display = 'none';
        modalEl.setAttribute('aria-hidden', 'true');
        document.body.classList.remove('modal-open');
        backdrop?.remove();
    }
}

async function openArticle(entityId, articleId) {
    if (!entityId && !articleId) return;
    const titleEl = document.getElementById('articleModalTitle');
    const subEl = document.getElementById('articleModalSubtitle');
    const bodyEl = document.getElementById('articleModalBody');

    titleEl.textContent = 'Loading article...';
    subEl.textContent = '';
    bodyEl.innerHTML = '<div class="text-center py-5"><div class="spinner-border text-primary" role="status"></div></div>';
    setModalVisible(true);

    try {
        const url = `/api?${entityId ? 'entity_id=' + encodeURIComponent(entityId) : 'article_id=' + encodeURIComponent(articleId)}`;
        const resp = await fetch(url);
        if (!resp.ok) throw new Error('Not found');
        const art = await resp.json();

        titleEl.textContent = art.title || 'Untitled';
        subEl.textContent = art.date_begin ? `Date: ${art.date_begin}` : (art.year ? `Year: ${art.year}` : '');

        let html = '';
        if (art.sections && art.sections.length > 0) {
            art.sections.forEach(s => {
                if (s.title) html += `<h5 class="mt-3 border-bottom pb-1">${s.title}</h5>`;
                html += `<p style="white-space: pre-line;">${s.content}</p>`;
            });
        } else {
            html = '<p class="text-muted">No content available for this entry.</p>';
        }
        bodyEl.innerHTML = html;
    } catch (e) {
        titleEl.textContent = 'Error';
        bodyEl.innerHTML = '<div class="alert alert-danger">Could not load article content.</div>';
    }
}

document.addEventListener('DOMContentLoaded', () => {
    initMap();

    document.getElementById('app-header').addEventListener('click', (e) => {
        if (!e.target.closest('#mobile-toggle-btn')) window.location.reload();
    });

    document.getElementById('btn-search').addEventListener('click', executeSearch);

    ['input-query', 'input-year', 'input-month', 'input-day'].forEach(id => {
        document.getElementById(id).addEventListener('keydown', (e) => {
            if (e.key === 'Enter') executeSearch();
        });
    });

    const closeModal = () => setModalVisible(false);
    document.getElementById('btn-modal-close').addEventListener('click', closeModal);
    document.getElementById('btn-modal-close-x').addEventListener('click', closeModal);
    document.getElementById('articleModal').addEventListener('click', (e) => {
        if (e.target.id === 'articleModal') closeModal();
    });

    const mobileBtn = document.getElementById('mobile-toggle-btn');
    mobileBtn.addEventListener('click', () => {
        const sidebar = document.getElementById('sidebar');
        isMobileMap = !isMobileMap;
        sidebar.classList.toggle('mobile-hidden', isMobileMap);
        mobileBtn.textContent = isMobileMap ? 'List' : 'Map';
        if (isMobileMap && map) map.resize();
    });
});
