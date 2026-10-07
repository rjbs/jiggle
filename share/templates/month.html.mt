% my $label = sub { my ($m) = @_; $site->month_name($m->{month}) . " $m->{year}" };
<%= $site->partial('_period_nav', {
  earlier => $earlier && { url => $site->month_url($earlier->{year}, $earlier->{month}), label => $label->($earlier) },
  later   => $later   && { url => $site->month_url($later->{year}, $later->{month}), label => $label->($later) },
}) %>
% my $n = $month->{photos}->@*;
<h1><%= $site->month_name($month->{month}) %> <a href="<%= $site->year_url($month->{year}) %>"><%= $month->{year} %></a>
  <span class="count"><%= $n %> photo<%= $n == 1 ? '' : 's' %></span></h1>
<%= $site->partial('_grid', { photos => $month->{photos} }) %>
