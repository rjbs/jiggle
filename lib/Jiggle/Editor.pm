package Jiggle::Editor;
use v5.36;

use Moo;

use Digest::SHA ();
use Jiggle ();
use Jiggle::Derive;
use Jiggle::Album;
use Jiggle::Photo;
use Jiggle::Query;
use Jiggle::TOML qw( datetime_with_offset );
use Mojo::JSON ();
use Mojo::Server::Daemon;
use Mojolicious;
use Path::Tiny ();

=head1 NAME

Jiggle::Editor - a local web app for reviewing and editing a batch of photos

=head1 SYNOPSIS

  my $editor = Jiggle::Editor->new({ library => $library, ids => \@ids });
  my $app    = $editor->app;     # a Mojolicious app
  say $editor->token;            # needed once, in the URL: /?token=...

=head1 DESCRIPTION

The editor serves a contact sheet of one batch of photos, fixed when it's
made: a list of ids, not a live query, so photos don't leave the batch when
they change.  Everything it serves is read from the library's files at the
time of the request, so a reload shows what's on disk.

Any web page in the browser can send requests to localhost, so every request
must carry the editor's token.  The first request gives it in the URL, and
gets it back as a cookie marked C<SameSite=Strict>, which the browser sends
only with requests from the editor's own pages.

=cut

has library => (is => 'ro', required => 1);

# The batch, in order.
has ids => (is => 'ro', required => 1);

# A label for the batch, like the query that chose it.
has label => (is => 'ro', default => '');

has token => (
  is => 'lazy',
  default => sub {
    open my $fh, '<:raw', '/dev/urandom' or die "can't read /dev/urandom: $!";
    read($fh, my $bytes, 16) == 16 or die "short read from /dev/urandom\n";
    return unpack 'H*', $bytes;
  },
);

# What remakes renditions when a write changes a photo's rotation, so the
# editor shows it turned.
has derive => (
  is => 'lazy',
  default => sub ($self) { Jiggle::Derive->new({ library => $self->library }) },
);

has share_dir => (
  is => 'ro',
  default => sub { Jiggle->share_dir },
);

my $COOKIE = 'jiggle_editor';

# The renditions the editor shows.  Others (like og.jpg) aren't needed, and
# the list keeps a request from naming arbitrary files.
my %SERVABLE = map {; $_ => 1 } qw( h480.webp 500.webp 1024.webp 2048.webp poster.png video.mp4 );

=method serve_query

  Jiggle::Editor->serve_query($library, [ 'pending' ], { port => 3001, open => 1 });

This picks a batch with L<Jiggle::Query>, then serves an editor for it on
127.0.0.1 until interrupted, printing its URL (with the token) and, if C<open>
is true, opening it in a browser.  It dies if nothing matches.

=cut

sub serve_query ($class, $library, $terms, $arg = {}) {
  my $query = Jiggle::Query->new({ library => $library, terms => $terms });

  my @ids = map {; $_->id } $query->photos;
  die "no photos match: @$terms\n" unless @ids;

  my $editor = $class->new({ library => $library, ids => \@ids, label => "@$terms" });

  my $listen = "http://127.0.0.1:" . ($arg->{port} // 3001);
  my $url    = "$listen/?token=" . $editor->token;

  my $daemon = Mojo::Server::Daemon->new(app => $editor->app, listen => [ $listen ], silent => 1);
  $daemon->start;

  say sprintf '%d photo(s); editing at %s', 0 + @ids, $url;
  say 'press control-C to stop';
  system('open', $url) if $^O eq 'darwin' && $arg->{open};

  $daemon->ioloop->start;
}

sub app ($self) {
  my $app = Mojolicious->new;
  $app->log->level('warn');
  $app->secrets([ $self->token ]);
  $app->types->type(webp => 'image/webp');

  my %in_batch = map {; $_ => 1 } $self->ids->@*;
  my $editor_dir = $self->share_dir->child('editor');

  my $r = $app->routes;

  $r->get('/' => sub ($c) {
    if (defined(my $token = $c->param('token'))) {
      return $c->render(text => 'bad token', status => 403) unless $token eq $self->token;
      $c->cookie($COOKIE => $token, { httponly => 1, samesite => 'Strict', path => '/' });
      return $c->redirect_to('/');
    }
    return $c->render(text => 'no token', status => 403)
      unless ($c->cookie($COOKIE) // '') eq $self->token;

    $c->res->headers->content_type('text/html; charset=utf-8');
    $c->res->headers->cache_control('no-cache');
    $c->render(data => $editor_dir->child('index.html')->slurp_raw);
  });

  my $authed = $r->under('/' => sub ($c) {
    return 1 if ($c->cookie($COOKIE) // '') eq $self->token;
    $c->render(json => { error => 'no token' }, status => 403);
    return undef;
  });

  $authed->get('/static/*file' => sub ($c) {
    my $file = $c->param('file');
    return $c->reply->not_found if $file =~ m{(?:\A|/)\.};
    my $path = $editor_dir->child('static', $file);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  $authed->get('/api/batch' => sub ($c) {
    $c->res->headers->cache_control('no-store');
    $c->render(json => $self->batch_data);
  });

  $authed->post('/api/write' => sub ($c) {
    my $result = $self->write_changes($c->req->json // {});
    $c->render(json => $result, status => $result->{status} // 200);
  });

  $authed->get('/r/:id/#name' => sub ($c) {
    my ($id, $name) = ($c->param('id'), $c->param('name'));
    return $c->reply->not_found unless $in_batch{$id} and $SERVABLE{$name};
    my $path = $self->library->derived_path($id, $name);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  return $app;
}

=method batch_data

This returns the batch as the editor's page gets it: the photos, in order, as
they are on disk now, the library's albums, and every tag in the library (as
of when the editor started), for suggestions.  Each photo has a C<version>,
the digest of its metadata file, which a write must present so that changes
made to the file since it was loaded aren't overwritten.

=cut

sub batch_data ($self) {
  my @albums    = $self->_albums;
  my %albums_of = $self->_albums_of(@albums);

  my @photos = map {; $self->_photo_record($_, \%albums_of) }
               grep {; -e $self->library->meta_path($_) } $self->ids->@*;

  my %tags = map {; $_ => 1 } map {; $_->tags->@* } $self->library->photos;

  return {
    label  => $self->label,
    photos => \@photos,
    tags   => [ sort { fc $a cmp fc $b } keys %tags ],
    albums => _album_list(@albums),
  };
}

=method photo_fields

This returns a photo's editable fields, and the facts shown beside them, as a
hash reference ready for JSON.

=cut

sub photo_fields ($self, $photo) {
  my $bool = sub ($v) { $v ? Mojo::JSON::true : Mojo::JSON::false };
  my $loc  = $photo->location;

  return {
    id          => $photo->id,
    type        => $photo->type,
    title       => $photo->title,
    description => $photo->description,
    tags        => [ $photo->tags->@* ],
    visibility  => $photo->visibility,
    pending     => $bool->($photo->pending),
    taken       => $photo->taken,
    rotate      => 0 + $photo->rotate,
    location    => $loc ? { private => $bool->($loc->{private}) } : undef,

    width       => 0 + $photo->width,
    height      => 0 + $photo->height,
    file        => $photo->original->{file},
    duration    => $photo->duration,
    added       => $photo->added_at,
    flickr_id   => $photo->flickr_id,
  };
}

=method write_changes

  my $result = $editor->write_changes({
    note   => 'optional; goes in the commit message',
    photos => [
      { id => $id, version => $version, changes => { title => '...', ... } },
      ...
    ],
  });

This writes the given changes to the photos' metadata files, and commits the
files it changed to F<meta>'s git repository.  It's all or nothing: nothing
is written unless every change is valid and every photo's C<version> matches
its file as it is now, so a file changed since the editor loaded it (by hand,
or a C<git pull>) is never overwritten.

The changes are to the fields L</photo_fields> gives, except that
C<location> is changed as C<location_private>, a boolean.  An empty C<taken>
removes it.

C<albums> is changed as C<< { add => [ ... ], remove => [ ... ] } >>, naming
albums by slug, or by the key of a new album the request makes:

  new_albums => [ { key => 'new-1', title => 'Berlin, 2026-07' } ]

Additions go at the end of the album, and are merged with the album's file as
it is now.  An album whose cover is removed gets its first photo as its cover.

A photo whose rotation changes has its renditions remade before this returns.

The result has C<photos>, each written photo as L</batch_data> would give it,
and C<commit>, the new commit's abbreviated id (undef if nothing changed, or
F<meta> isn't a git repository).  It also has C<albums>, every album, and
C<new_albums>, which maps each new album's key to its slug.  A failure has a C<status> (409 for a
conflict, 400 for anything else) and an C<error>, and a conflict lists the
photos in C<conflicts>.

=cut

my %TEXT_FIELD = map {; $_ => 1 } qw( title description );

# A TOML datetime, local or with an offset, to the second.
my $DATETIME = qr/\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[-+][0-9]{2}:[0-9]{2})?\z/;

sub write_changes ($self, $request) {
  my %in_batch = map {; $_ => 1 } $self->ids->@*;
  my (@plan, @conflicts, %fields_changed, %touched);

  # Albums as they are on disk now, and any the request makes.  Membership
  # changes are applied to these, as additions and removals, so a change
  # made to an album file since the page loaded is kept, not overwritten.
  my %album = map {; $_->slug => $_ } $self->_albums;
  my %before = map {; $_ => $album{$_}->as_toml } keys %album;
  my %slug_for_key;
  my %taken = map {; $_ => 1 } keys %album;
  for my $spec (($request->{new_albums} // [])->@*) {
    my ($key, $title) = ($spec->{key} // '', $spec->{title} // '');
    $title =~ s/\A\s+|\s+\z//g;
    return _failure(400, 'a new album needs a key and a title') unless length $key and length $title;
    my $slug = Jiggle::Album->unique_slug($title, \%taken);
    $slug_for_key{$key} = $slug;
    $album{$slug} = Jiggle::Album->new({
      slug    => $slug,
      title   => $title,
      created => datetime_with_offset(time),
    });
  }

  my (%add_to, %remove_from);
  for my $item (($request->{photos} // [])->@*) {
    my $id = $item->{id} // '';
    return _failure(400, "not in this batch: $id") unless $in_batch{$id};

    my $file  = $self->library->meta_path($id);
    my $bytes = $file->slurp_raw;
    if (Digest::SHA::sha1_hex($bytes) ne ($item->{version} // '')) {
      push @conflicts, $id;
      next;
    }

    my $photo = Jiggle::Photo->from_toml_file($file);
    my %attr  = %$photo;

    my %changes = ($item->{changes} // {})->%*;
    if (my $albums = delete $changes{albums}) {
      return _failure(400, "$id: albums must be { add, remove }") unless ref $albums eq 'HASH';
      for my $op (qw( add remove )) {
        my $ops = $op eq 'add' ? \%add_to : \%remove_from;
        for my $name (($albums->{$op} // [])->@*) {
          my $slug = $slug_for_key{$name} // $name;
          return _failure(400, "$id: no album $name") unless $album{$slug};
          push $ops->{$slug}->@*, $id;
        }
      }
      $fields_changed{albums} = 1;
      $touched{$id} = 1;
    }

    for my $field (sort keys %changes) {
      my $error = $self->_apply_change(\%attr, $field, $changes{$field});
      return _failure(400, "$id: $error") if $error;
      $fields_changed{$field} = 1;
    }

    my $new = eval { Jiggle::Photo->new(\%attr) };
    return _failure(400, "$id: " . ($@ =~ s/ at .*//sr)) unless $new;

    if ($new->as_toml ne $bytes) {
      push @plan, { file => $file, text => $new->as_toml, turned => $new->rotate != $photo->rotate, photo => $new };
      $touched{$id} = 1;
    }
  }

  return _failure(409, 'changed on disk since loading: ' . join(q{, }, @conflicts),
    conflicts => \@conflicts) if @conflicts;

  my @new_titles;
  for my $slug (sort keys %album) {
    my $old = $album{$slug};
    my %gone = map {; $_ => 1 } ($remove_from{$slug} // [])->@*;
    my @photos = grep {; ! $gone{$_} } $old->photos->@*;
    my %have = map {; $_ => 1 } @photos;
    push @photos, grep {; ! $have{$_}++ } ($add_to{$slug} // [])->@*;

    # A new album with nothing in it was made and then abandoned.
    next if ! $before{$slug} and ! @photos;

    my $cover = $old->cover;
    $cover = $photos[0] unless defined $cover and grep {; $_ eq $cover } @photos;

    my %arg = (%$old, photos => \@photos);
    delete $arg{cover};
    $arg{cover} = $cover if defined $cover;
    my $new = Jiggle::Album->new(\%arg);

    my $text = $new->as_toml;
    next if defined $before{$slug} and $text eq $before{$slug};

    push @new_titles, $new->title unless $before{$slug};
    push @plan, { file => $self->library->albums_dir->child("$slug.toml"), text => $text };
  }

  $self->library->albums_dir->mkpath if @plan;
  for my $step (@plan) {
    my $tmp = $step->{file}->sibling('.' . $step->{file}->basename . '.tmp');
    $tmp->spew_utf8($step->{text});
    $tmp->move($step->{file});
  }

  my $commit;
  if (@plan) {
    my $message = sprintf 'edit %d photo(s): %s',
      scalar keys %touched, join q{, }, sort keys %fields_changed;
    $message .= '; new album: ' . join q{, }, @new_titles if @new_titles;
    $message .= "\n\n$request->{note}" if ($request->{note} // '') =~ /\S/;
    $commit = $self->library->commit_meta($message, map {; $_->{file} } @plan);
  }

  # The write is done (and committed) whether or not this works; a photo
  # whose renditions fail is noted in derive's manifest, as always.
  my @turned = map {; $_->{photo} } grep {; $_->{turned} } @plan;
  $self->derive->derive_photos(@turned) if @turned;

  my @albums    = $self->_albums;
  my %albums_of = $self->_albums_of(@albums);
  return {
    commit => $commit,
    photos => [
      map  {; $self->_photo_record($_, \%albums_of) }
      grep {; $touched{$_} } $self->ids->@*
    ],
    albums     => _album_list(@albums),
    new_albums => \%slug_for_key,
  };
}

sub _album_list (@albums) {
  return [
    sort {; fc $a->{title} cmp fc $b->{title} }
    map  {; { slug => $_->slug, title => $_->title } } @albums
  ];
}

sub _failure ($status, $error, %more) {
  return { status => $status, error => $error, %more };
}

# Applies one change to a photo's attributes, returning an error, if any.
sub _apply_change ($self, $attr, $field, $value) {
  if ($TEXT_FIELD{$field}) {
    return "$field must be a string" if ref $value or ! defined $value;
    $attr->{$field} = $value;
  }
  elsif ($field eq 'tags') {
    return 'tags must be a list' unless ref $value eq 'ARRAY';
    my (%seen, @tags);
    for my $tag (@$value) {
      return 'tags must be strings' if ref $tag or ! defined $tag;
      $tag =~ s/\A\s+|\s+\z//g;
      push @tags, $tag if length $tag and ! $seen{$tag}++;
    }
    $attr->{tags} = \@tags;
  }
  elsif ($field eq 'visibility') {
    return "unknown visibility" unless ($value // '') =~ /\A(?:public|private)\z/;
    $attr->{visibility} = $value;
  }
  elsif ($field eq 'pending') {
    $attr->{pending} = $value ? 1 : 0;
  }
  elsif ($field eq 'taken') {
    $value //= '';
    return "taken must be a datetime like 2026-09-30T10:15:00" if length $value and $value !~ $DATETIME;
    if (length $value) { $attr->{taken} = $value } else { delete $attr->{taken} }
  }
  elsif ($field eq 'rotate') {
    return 'rotate must be 0, 90, 180, or 270'
      unless defined $value and ! ref $value and $value =~ /\A(?:0|90|180|270)\z/;
    $attr->{rotate} = 0 + $value;
  }
  elsif ($field eq 'location_private') {
    return 'no location to make private' unless $attr->{location};
    my %loc = $attr->{location}->%*;
    if ($value) { $loc{private} = 1 } else { delete $loc{private} }
    $attr->{location} = \%loc;
  }
  else {
    return "can't change $field";
  }

  return;
}

# One photo, as the page gets it, read from its file now.
sub _photo_record ($self, $id, $albums_of) {
  my $file  = $self->library->meta_path($id);
  my $bytes = $file->slurp_raw;
  my $photo = Jiggle::Photo->from_toml_file($file);
  return {
    $self->photo_fields($photo)->%*,
    albums  => [ sort { $a cmp $b } ($albums_of->{$id} // [])->@* ],
    version => Digest::SHA::sha1_hex($bytes),
  };
}

# { photo id => [ album slug, ... ] }
sub _albums_of ($self, @albums) {
  @albums = $self->_albums unless @albums;
  my %albums_of;
  for my $album (@albums) {
    push $albums_of{$_}->@*, $album->slug for $album->photos->@*;
  }
  return %albums_of;
}

sub _albums ($self) {
  my $dir = $self->library->albums_dir;
  return () unless -d $dir;
  return map {; Jiggle::Album->from_toml_file($_) } sort $dir->children(qr/\.toml\z/);
}

1;
