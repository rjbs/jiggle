// Maps for jiggle, following the /travel map on rjbs.cloud: MapLibre GL with
// OpenFreeMap's key-less "liberty" style.  -- claude, 2026-09-26
(function () {
  var STYLE = 'https://tiles.openfreemap.org/styles/liberty';

  function makeMap(options) {
    var map = new maplibregl.Map(Object.assign({
      style: STYLE,
      attributionControl: false
    }, options));

    map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-left');

    // The liberty style declares its own attribution on its source, so adding
    // it again as customAttribution would show it twice.
    map.addControl(new maplibregl.AttributionControl());

    // OpenFreeMap labels places in their local language.  Prefer English,
    // falling back to the local name.
    map.on('load', function () {
      map.getStyle().layers.forEach(function (layer) {
        if (layer.type === 'symbol' && layer.layout && layer.layout['text-field']) {
          map.setLayoutProperty(layer.id, 'text-field',
            ['coalesce', ['get', 'name:en'], ['get', 'name']]);
        }
      });
    });

    return map;
  }

  // One pin, for a photo page.
  function single(id, lat, lon) {
    var map = makeMap({ container: id, center: [lon, lat], zoom: 13 });
    new maplibregl.Marker({ color: '#880088' }).setLngLat([lon, lat]).addTo(map);
  }

  // Every photo.  There may be thousands, far too many for DOM markers, so
  // they're a clustered GeoJSON source drawn with layers.
  function all(id, url) {
    fetch(url).then(function (r) { return r.json(); }).then(function (data) {
      var options = { container: id };

      if (data.features.length) {
        var bounds = new maplibregl.LngLatBounds();
        data.features.forEach(function (f) { bounds.extend(f.geometry.coordinates); });
        options.bounds = bounds;
        options.fitBoundsOptions = { padding: 60, maxZoom: 14 };
      } else {
        options.center = [0, 20];
        options.zoom = 1;
      }

      var map = makeMap(options);

      map.on('load', function () {
        map.addSource('photos', {
          type: 'geojson',
          data: data,
          cluster: true,
          clusterRadius: 40,
          clusterMaxZoom: 17
        });

        map.addLayer({
          id: 'clusters',
          type: 'circle',
          source: 'photos',
          filter: ['has', 'point_count'],
          paint: {
            'circle-color': '#880088',
            'circle-opacity': 0.8,
            'circle-stroke-color': '#ffffff',
            'circle-stroke-width': 2,
            'circle-radius': ['step', ['get', 'point_count'], 14, 10, 18, 100, 24, 1000, 30]
          }
        });

        map.addLayer({
          id: 'cluster-count',
          type: 'symbol',
          source: 'photos',
          filter: ['has', 'point_count'],
          layout: {
            'text-field': ['get', 'point_count_abbreviated'],
            'text-font': ['Noto Sans Bold'],
            'text-size': 12
          },
          paint: { 'text-color': '#ffffff' }
        });

        map.addLayer({
          id: 'photo',
          type: 'circle',
          source: 'photos',
          filter: ['!', ['has', 'point_count']],
          paint: {
            'circle-color': '#880088',
            'circle-radius': 7,
            'circle-stroke-color': '#ffffff',
            'circle-stroke-width': 2
          }
        });

        map.on('click', 'clusters', function (e) {
          var feature = map.queryRenderedFeatures(e.point, { layers: ['clusters'] })[0];
          map.getSource('photos')
            .getClusterExpansionZoom(feature.properties.cluster_id)
            .then(function (zoom) {
              map.easeTo({ center: feature.geometry.coordinates, zoom: zoom });
            });
        });

        map.on('click', 'photo', function (e) {
          var f = e.features[0];
          var p = f.properties;
          var html = document.createElement('a');
          html.href = p.url;
          html.className = 'map-popup';
          var img = document.createElement('img');
          img.src = p.thumb;
          img.width = 150;
          img.height = 150;
          var title = document.createElement('span');
          title.textContent = p.title;
          html.appendChild(img);
          html.appendChild(title);

          new maplibregl.Popup({ maxWidth: '180px' })
            .setLngLat(f.geometry.coordinates)
            .setDOMContent(html)
            .addTo(map);
        });

        ['clusters', 'photo'].forEach(function (layer) {
          map.on('mouseenter', layer, function () { map.getCanvas().style.cursor = 'pointer'; });
          map.on('mouseleave', layer, function () { map.getCanvas().style.cursor = ''; });
        });
      });
    });
  }

  window.jiggleMap = { single: single, all: all };
}());
