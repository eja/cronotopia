let map;
let initialMapCenter = [9.0100, 40.1200];
let initialMapZoom = 4;

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

function haversineDistance(lat1, lon1, lat2, lon2) {
    const R = 6371.0;
    const dLat = (lat2 - lat1) * Math.PI / 180.0;
    const dLon = (lon2 - lon1) * Math.PI / 180.0;
    const a = Math.sin(dLat / 2) ** 2 + Math.cos(lat1 * Math.PI / 180.0) * Math.cos(lat2 * Math.PI / 180.0) * Math.sin(dLon / 2) ** 2;
    return R * 2 * Math.atan2(Math.sqrt(a), Math.sqrt(1 - a));
}

function getMapRadiusKm() {
    const bounds = map.getBounds();
    const center = map.getCenter();
    const dLat = haversineDistance(center.lat, center.lng, bounds.getNorth(), center.lng);
    const dLng = haversineDistance(center.lat, center.lng, center.lat, bounds.getEast());
    const r = Math.min(dLat, dLng) * 0.8;
    return Math.max(0.01, parseFloat(r.toFixed(3)));
}

function createGeoJSONCircle(center, radiusKm, points = 64) {
    const coords = { latitude: center.lat, longitude: center.lng };
    const ret = [];
    const distanceX = radiusKm / (111.320 * Math.cos(coords.latitude * Math.PI / 180));
    const distanceY = radiusKm / 110.574;
    for (let i = 0; i < points; i++) {
        const theta = (i / points) * (2 * Math.PI);
        ret.push([coords.longitude + distanceX * Math.cos(theta), coords.latitude + distanceY * Math.sin(theta)]);
    }
    ret.push(ret[0]);
    return {
        type: 'Feature',
        geometry: { type: 'Polygon', coordinates: [ret] }
    };
}

function updateSearchCircle(visible, center, radiusKm) {
    if (!map) return;
    const source = map.getSource('search-circle-source');
    if (!source) return;
    if (!visible || !center || !radiusKm) {
        source.setData({ type: 'FeatureCollection', features: [] });
        return;
    }
    source.setData(createGeoJSONCircle(center, radiusKm));
}

function setMatchingLayersVisible(pattern, visible, exclude = []) {
    if (!map || !map.getStyle()) return;
    const val = visible ? 'visible' : 'none';
    const layers = map.getStyle().layers || [];
    layers.forEach(l => {
        if (exclude.includes(l.id)) return;
        if (pattern.test(l.id)) {
            map.setLayoutProperty(l.id, 'visibility', val);
        }
    });
}

function setContoursVisible(visible) {
    if (!map) return;
    const val = visible ? 'visible' : 'none';
    ['contour-lines', 'contour-labels-major', 'contour-labels-all'].forEach(id => {
        if (map.getLayer(id)) map.setLayoutProperty(id, 'visibility', val);
    });
}

function setElevationVisible(visible) {
    if (!map) return;
    if (visible) {
        map.setTerrain({ source: 'terrain-source', exaggeration: 1.5 });
        if (map.getLayer('hills')) map.setLayoutProperty('hills', 'visibility', 'visible');
    } else {
        map.setTerrain(null);
        if (map.getLayer('hills')) map.setLayoutProperty('hills', 'visibility', 'none');
    }
}

function setBuildingsVisible(visible) {
    setMatchingLayersVisible(/building/i, visible);
}

function setPoisVisible(visible) {
    setMatchingLayersVisible(/poi|place|label/i, visible, ['contour-labels-major', 'contour-labels-all']);
}

function setRoadsVisible(visible) {
    setMatchingLayersVisible(/road|transit|highway/i, visible);
}

function setLayerModalVisible(show) {
    const modalEl = document.getElementById('layerModal');
    if (!modalEl) return;
    let backdrop = document.getElementById('layer-modal-backdrop');
    if (show) {
        modalEl.style.display = 'block';
        modalEl.removeAttribute('aria-hidden');
        modalEl.classList.add('show');
        document.body.classList.add('modal-open');
        if (!backdrop) {
            backdrop = document.createElement('div');
            backdrop.id = 'layer-modal-backdrop';
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

class LayerSettingsControl {
    onAdd(mapInstance) {
        this._map = mapInstance;
        this._container = document.createElement('div');
        this._container.className = 'maplibregl-ctrl maplibregl-ctrl-group';
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'maplibregl-ctrl-icon d-flex align-items-center justify-content-center';
        btn.title = 'Map Layers';
        btn.setAttribute('aria-label', 'Map Layers');
        btn.innerHTML = `<svg viewBox="0 0 24 24" width="18" height="18" fill="#333"><path d="M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.09.63-.09.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z"/></svg>`;
        btn.addEventListener('click', () => setLayerModalVisible(true));
        this._container.appendChild(btn);
        return this._container;
    }
    onRemove() {
        this._container?.parentNode?.removeChild(this._container);
        this._map = undefined;
    }
}

async function initMap() {
    const mapLoader = document.getElementById('map-loader');
    let mapConfig = { min_zoom: 0, max_zoom: 14, center_lng: initialMapCenter[0], center_lat: initialMapCenter[1], zoom: 4 };

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
    } catch (_) {
        mapConfig.max_zoom = await probeMaxZoom(mapConfig.center_lng, mapConfig.center_lat);
    }

    initialMapCenter = [mapConfig.center_lng, mapConfig.center_lat];
    initialMapZoom = mapConfig.zoom;

    const demSource = new mlcontour.DemSource({
        url: window.location.origin + '/tiles/elevations/{z}/{x}/{y}.png',
        encoding: 'terrarium',
        maxzoom: 7,
        worker: true
    });
    demSource.setupMaplibre(maplibregl);

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
        maxZoom: 22,
        maxPitch: 85
    });

    map.addControl(new maplibregl.GeolocateControl({ positionOptions: { enableHighAccuracy: true }, trackUserLocation: true, showUserHeading: true }), 'top-right');
    map.addControl(new LayerSettingsControl(), 'top-right');
    map.addControl(new maplibregl.NavigationControl({ visualizePitch: true }), 'bottom-right');
    map.addControl(new maplibregl.ScaleControl({ unit: 'metric' }), 'bottom-left');

    const hideLoader = () => {
        if (mapLoader) {
            mapLoader.style.transition = 'opacity 0.3s ease';
            mapLoader.style.opacity = '0';
            setTimeout(() => mapLoader.remove(), 300);
        }
    };

    map.once('load', () => {
        map.addSource('terrain-source', {
            type: 'raster-dem',
            tiles: [demSource.sharedDemProtocolUrl],
            encoding: 'terrarium',
            tileSize: 256,
            maxzoom: 7
        });
        map.setTerrain({ source: 'terrain-source', exaggeration: 1.5 });

        map.addLayer({
            id: 'hills',
            type: 'hillshade',
            source: 'terrain-source',
            paint: {
                'hillshade-exaggeration': 0.25,
                'hillshade-shadow-color': '#1a2228',
                'hillshade-highlight-color': '#ffffff',
                'hillshade-accent-color': '#334455'
            }
        });

        if (!document.getElementById('layer-toggle-elevation')?.checked) {
            setElevationVisible(false);
        }

        map.addSource('contour-source', {
            type: 'vector',
            tiles: [
                demSource.contourProtocolUrl({
                    thresholds: {
                        2: [500, 1000],
                        4: [200, 500],
                        6: [100, 200],
                        8: [50, 100]
                    },
                    overzoom: 1,
                    elevationKey: 'ele',
                    levelKey: 'level',
                    contourLayer: 'contours'
                })
            ],
            minzoom: 2,
            maxzoom: Math.max(mapConfig.max_zoom, 9)
        });

        map.addLayer({
            id: 'contour-lines',
            type: 'line',
            source: 'contour-source',
            'source-layer': 'contours',
            paint: {
                'line-color': [
                    'case',
                    ['<', ['get', 'ele'], 0], 'rgba(40, 100, 170, 0.45)',
                    'rgba(120, 80, 50, 0.5)'
                ],
                'line-width': ['match', ['get', 'level'], 1, 1.4, 0.6]
            }
        });

        map.addLayer({
            id: 'contour-labels-major',
            type: 'symbol',
            source: 'contour-source',
            'source-layer': 'contours',
            minzoom: 2,
            maxzoom: 9,
            filter: ['==', ['get', 'level'], 1],
            layout: {
                'symbol-placement': 'line',
                'text-field': ['concat', ['to-string', ['get', 'ele']], 'm'],
                'text-size': 9,
                'text-max-angle': 85
            },
            paint: {
                'text-color': '#2b2620',
                'text-halo-color': 'rgba(255, 255, 255, 0.95)',
                'text-halo-width': 2
            }
        });

        map.addLayer({
            id: 'contour-labels-all',
            type: 'symbol',
            source: 'contour-source',
            'source-layer': 'contours',
            minzoom: 9,
            layout: {
                'symbol-placement': 'line',
                'text-field': ['concat', ['to-string', ['get', 'ele']], 'm'],
                'text-size': 9,
                'text-max-angle': 85,
                'text-padding': 1
            },
            paint: {
                'text-color': '#2b2620',
                'text-halo-color': 'rgba(255, 255, 255, 0.95)',
                'text-halo-width': 2
            }
        });

        if (!document.getElementById('layer-toggle-contours')?.checked) {
            setContoursVisible(false);
        }

        map.addSource('search-circle-source', {
            type: 'geojson',
            data: { type: 'FeatureCollection', features: [] }
        });
        map.addLayer({
            id: 'search-circle-fill',
            type: 'fill',
            source: 'search-circle-source',
            paint: { 'fill-color': '#0d6efd', 'fill-opacity': 0.12 }
        });
        map.addLayer({
            id: 'search-circle-stroke',
            type: 'line',
            source: 'search-circle-source',
            paint: { 'line-color': '#0d6efd', 'line-width': 2, 'line-dasharray': [2, 2] }
        });

        map.addSource('spider-legs-source', {
            type: 'geojson',
            data: { type: 'FeatureCollection', features: [] }
        });
        map.addLayer({
            id: 'spider-legs-layer',
            type: 'line',
            source: 'spider-legs-source',
            paint: { 'line-color': '#495057', 'line-width': 1.5, 'line-dasharray': [2, 2] }
        });

        map.on('move', () => {
            if (document.getElementById('select-visible-map').value === 'true') {
                updateSearchCircle(true, map.getCenter(), getMapRadiusKm());
            }
        });

        map.on('click', () => {
            closeActivePopup();
            if (activeSpiderKey) collapseSpider();
        });

        map.on('zoomstart', () => {
            closeActivePopup();
            collapseSpider();
        });
        map.on('movestart', () => {
            closeActivePopup();
            collapseSpider();
        });

        map.resize();
        hideLoader();
    });

    setTimeout(hideLoader, 4000);
}
