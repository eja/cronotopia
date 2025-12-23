var propertyMap = {
    "en": {
        "19": "Place of birth", "20": "Place of death", "625": "Location",
        "569": "Date of birth", "570": "Date of death", "571": "Inception",
        "575": "Discovery or invention time", "576": "Dissolved date",
        "577": "Publication date", "580": "Start time", "582": "End time",
        "585": "Point in time", "729": "Service entry", "730": "Service retirement",
        "746": "Date of disappearance", "1191": "Date of first performance",
        "1249": "Time of earliest written record", "1319": "Earliest date",
        "1326": "Latest date", "1619": "Date of official opening",
        "2031": "Start of the working period", "2032": "End of the working period",
        "2669": "Discontinued date", "2754": "Production date",
        "3999": "Date of official closure", "5204": "Date of commercialization",
        "6949": "Announcement date", "7124": "Date of the first one",
        "7125": "Date of the latest one", "7588": "Effective date",
        "7589": "Enacted date", "9667": "Date of resignation", "10135": "Recording date",
    }
};

var map, language = "en";

document.addEventListener("DOMContentLoaded", function() {
    const cfg = window.MAP_CONFIG || { center: [0, 0], zoom: 1, minZoom: 0, maxZoom: 14 };

    document.getElementById("day").value = new Date().getDate();
    document.getElementById("month").value = new Date().getMonth()+1;

    map = new maplibregl.Map({
        container: "map",
        center: cfg.center,
        zoom: cfg.zoom,
        style: {
            version: 8,
            glyphs: window.location.origin + "/fonts/{fontstack}/{range}.pbf",
            sprite: window.location.origin + "/pics/light",
            sources: {
                "local_mbtiles": {
                    type: "vector",
                    tiles: [window.location.origin + "/tiles/{z}/{x}/{y}.pbf"],
                    minzoom: cfg.minZoom, maxzoom: cfg.maxZoom 
                }
            },
            layers: basemaps.layers("local_mbtiles", basemaps.namedFlavor("light"), { lang: language })
        }
    });

    if ("geolocation" in navigator) {
        navigator.geolocation.getCurrentPosition(function(position) {
            const userLon = position.coords.longitude;
            const userLat = position.coords.latitude;
            const startZoom = cfg.maxZoom;

            map.flyTo({
                center: [userLon, userLat],
                zoom: startZoom,
                speed: 1.2,
                essential: true
            });

            const latEl = document.getElementById("latitude");
            const lonEl = document.getElementById("longitude");
            if (latEl) latEl.value = userLat.toFixed(6);
            if (lonEl) lonEl.value = userLon.toFixed(6);
        }, function(err) {
            console.warn("GPS Access Denied:", err);
        });
    }

    map.addControl(new maplibregl.NavigationControl(), 'bottom-right');

    map.on('load', function() {
        map.addSource('results', {
            type: 'geojson',
            data: { type: 'FeatureCollection', features: [] },
            cluster: false 
        });

        map.addLayer({
            id: 'unclustered-point',
            type: 'circle',
            source: 'results',
            paint: {
                'circle-color': '#0d6efd',
                'circle-radius': 9,
                'circle-stroke-width': 3,
                'circle-stroke-color': '#fff'
            }
        });

        map.on('move', function() {
            const center = map.getCenter();
            const latEl = document.getElementById("latitude");
            const lonEl = document.getElementById("longitude");
            if (latEl) latEl.value = center.lat.toFixed(6);
            if (lonEl) lonEl.value = center.lng.toFixed(6);
        });

        map.on('click', 'unclustered-point', function(e) {
            var features = map.queryRenderedFeatures(e.point, { layers: ['unclustered-point'] });
            var listHtml = features.map(f => f.properties.popupHtml).join('<hr>');

            var cardHtml = `
                <div class="card shadow border-0 overflow-hidden" style="width: 320px;">
                    <div class="card-header bg-light d-flex align-items-center justify-content-end py-2 border-bottom-0">
                        <button type="button" class="btn-close small" onclick="map.getCanvas().click()" aria-label="Close" style="font-size: 0.7rem;"></button>
                    </div>
                    <div class="card-body overflow-auto p-3" style="max-height: 350px;">
                        ${listHtml}
                    </div>
                    ${features.length > 1 ? `<div class="card-footer py-1 bg-light text-center small text-secondary fw-bold border-top-0">${features.length} Items Found</div>` : ''}
                </div>`;

            var popup = new maplibregl.Popup({ 
                maxWidth: 'none', 
                offset: [0, -10],
                closeButton: false
            })
                .setLngLat(features[0].geometry.coordinates)
                .setHTML(cardHtml)
                .addTo(map);

            const closeBtn = document.querySelector('.btn-close');
            if (closeBtn) {
                closeBtn.addEventListener('click', () => popup.remove());
            }
        });

        map.on('mouseenter', 'unclustered-point', () => map.getCanvas().style.cursor = 'pointer');
        map.on('mouseleave', 'unclustered-point', () => map.getCanvas().style.cursor = '');
    });
});

function createFeature(o, lon, lat) {
    var prop = (propertyMap[language] && propertyMap[language][o.code]) ? propertyMap[language][o.code] : "Property " + o.code;
    var wikiUrl = `https://${language}.wikipedia.org/wiki/${encodeURIComponent(o.label.replace(/ /g, "_"))}`;

    let date = ''
    if (o.day) date += o.day
    if (o.month) date += (date && '/') + o.month
    if (o.year) date += (date && '/') + o.year

    var txt = `
        <div class="item">
            <h6 class="mb-1">
                <a href="${wikiUrl}" target="_blank" class="text-decoration-none text-secondary fw-bold">${o.label}</a>
            </h6>
            <div class="small text-muted mb-2">${o.data || ""}</div>
            <div class="text-end pt-1 mt-1">
                <small class="text-secondary fw-bold">${prop}:</small>
                <small class="fw-bold text-dark">${date}</small>
            </div>
        </div>`;

    return { 
        "type": "Feature", 
        "properties": { "popupHtml": txt }, 
        "geometry": { "type": "Point", "coordinates": [lon, lat] }
    };
}

function spiderfyData(data) {
    var groups = {};
    data.forEach(o => {
        var key = parseFloat(o.latitude).toFixed(6) + "," + parseFloat(o.longitude).toFixed(6);
        if (!groups[key]) groups[key] = [];
        groups[key].push(o);
    });
    var resultFeatures = [];
    var radius = 0.001; 
    for (var key in groups) {
        var items = groups[key];
        var lon = parseFloat(items[0].longitude), lat = parseFloat(items[0].latitude);
        if (items.length === 1) {
            resultFeatures.push(createFeature(items[0], lon, lat));
        } else {
            items.forEach((item, i) => {
                var angle = (i * 2 * Math.PI) / items.length;
                resultFeatures.push(createFeature(item, lon + radius * Math.cos(angle), lat + radius * Math.sin(angle)));
            });
        }
    }
    return resultFeatures;
}


function loadData() {
    const buttonSpinner = document.getElementById('buttonSpinner');
    const buttonText = document.getElementById('buttonText');
    if (buttonSpinner) buttonSpinner.classList.remove('d-none');
    if (buttonText) buttonText.textContent = 'Searching...';
    
    const searchButton = document.querySelector('button[onclick="loadData()"]');
    if (searchButton) searchButton.disabled = true;
 
   var params = {
        day: document.getElementById("day")?.value || 0,
        month: document.getElementById("month")?.value || 0,
        year: document.getElementById("year")?.value || 0,
        latitude: document.getElementById("latitude")?.value || 0,
        longitude: document.getElementById("longitude")?.value || 0,
        range: document.getElementById("range")?.value || 50,
        query: document.getElementById("query")?.value || "",
        limit: 50
    };

    fetch("/api?" + new URLSearchParams(params).toString())
        .then(res => res.json())
        .then(data => {
            if (buttonSpinner) buttonSpinner.classList.add('d-none');
            if (buttonText) buttonText.textContent = 'Search';
            if (searchButton) searchButton.disabled = false;
            if (!data || data.length === 0) {
              var center = map.getCenter();
              new maplibregl.Popup().setLngLat(center).setHTML('<div class="bg-light p-3"><strong>No results found</strong></div>').addTo(map);
              return;
            }
            var limitedData = data.slice(0, 50);
            
            var features = spiderfyData(limitedData);
            if (map.getSource('results')) {
                map.getSource('results').setData({ "type": "FeatureCollection", "features": features });
                if (features.length > 0) {
                    var bounds = new maplibregl.LngLatBounds();
                    features.forEach(f => bounds.extend(f.geometry.coordinates));
                    map.fitBounds(bounds, { padding: 60, maxZoom: 12 });
                }
            }
        });
}
