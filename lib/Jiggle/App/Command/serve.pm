package Jiggle::App::Command::serve;
use v5.36;

use Jiggle::App -command;

use Mojolicious;
use Mojo::Server::Daemon;

sub abstract { 'serve the built site locally, for previewing' }

sub opt_spec {
  return (
    [ 'port|p=i', 'port to listen on', { default => 3000 } ],
  );
}

sub execute ($self, $opt, $args) {
  my $root = $self->library->root->child('site');
  die "no site at $root; run jiggle build first\n" unless -d $root;

  my $app = Mojolicious->new;
  $app->log->level('warn');
  $app->static->paths([ "$root" ]);
  $app->types->type(webp => 'image/webp');
  $app->types->type(geojson => 'application/geo+json');

  # Mojolicious serves files, but not a directory's index.html, which a
  # static host (and so the site's links) will expect.
  $app->hook(before_dispatch => sub ($c) {
    my $path = $c->req->url->path;
    $path->merge('index.html') if $path->trailing_slash || "$path" eq '/';
  });

  # Without Cache-Control, a browser may guess how long its copy of a file
  # stays fresh, and go on using a stale stylesheet after a rebuild.  For
  # previewing, always revalidate; an unchanged file costs only a 304.
  $app->hook(after_static => sub ($c) {
    $c->res->headers->cache_control('no-cache');
  });

  # The site's own 404 page, as the real web server will serve it.
  my $not_found = $root->child('404.html');
  $app->routes->any('/*whatever' => { whatever => '' } => sub ($c) {
    return $c->render(text => 'not found', status => 404) unless -e $not_found;
    $c->res->headers->content_type('text/html; charset=utf-8');
    $c->render(data => $not_found->slurp_raw, status => 404);
  });

  my $url = "http://127.0.0.1:" . $opt->port;
  say "serving $root at $url/";

  Mojo::Server::Daemon->new(app => $app, listen => [ $url ])->run;
}

1;
